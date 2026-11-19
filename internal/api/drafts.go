package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func draftDate(w http.ResponseWriter, r *http.Request) (string, bool) {
	date := r.PathValue("date")
	if _, err := edition.ParseDate(date); err != nil {
		fail(w, 400, "date must be YYYY-MM-DD")
		return "", false
	}
	return date, true
}
func validation(ed *edition.Edition, p config.Paper) ([]problem, []problem) {
	problems, warnings := []problem{}, []problem{}
	for _, v := range edition.Validate(ed, p.Rules()) {
		if v.Severity == edition.Error {
			problems = append(problems, problem{v.Path, v.Rule})
		} else {
			warnings = append(warnings, problem{v.Path, v.Rule})
		}
	}
	return problems, warnings
}
func draftError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDraftConflict):
		fail(w, 409, "draft revision or state conflict")
	case errors.Is(err, sql.ErrNoRows):
		fail(w, 404, "no such draft")
	default:
		fail(w, 500, "storage failed")
	}
}
func (s *Server) submitDraft(w http.ResponseWriter, r *http.Request) {
	date, ok := draftDate(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, 413, "body must be at most 1 MB")
		return
	}
	ed, err := edition.Decode(bytes.NewReader(raw))
	if err != nil {
		jsonResponse(w, 422, map[string]any{"errors": problemList([]string{err.Error()})})
		return
	}
	p := current(r)
	problems, warnings := validation(ed, p.paper)
	warnings = append(warnings, s.sourceWarnings(r.Context(), p.scope, p.paper, ed)...)
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems, "warnings": warnings})
		return
	}
	rev, err := s.Store.SubmitDraft(r.Context(), p.scope, date, p.keyID, ed)
	if err != nil {
		draftError(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"revision": rev, "warnings": warnings})
}
func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	date, ok := draftDate(w, r)
	if !ok {
		return
	}
	d, err := s.Store.Draft(r.Context(), current(r).scope, date)
	if err != nil {
		draftError(w, err)
		return
	}
	jsonResponse(w, 200, d)
}
func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state != "" && state != "gathering" && state != "drafted" && state != "in_review" && state != "published" && state != "held" {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"state", "must be one of gathering, drafted, in_review, published, held"}}})
		return
	}
	drafts, err := s.Store.Drafts(r.Context(), current(r).scope, state)
	if err != nil {
		draftError(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"drafts": drafts})
}

// objectRequest preserves each field's raw JSON for exact validation paths and
// rejects trailing JSON, null, arrays and unknown fields in action-specific code.
func objectRequest(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, 413, "body must be at most 1 MB")
		return nil, false
	}
	var object map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if dec.Decode(&object) != nil || object == nil {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"$", "must be a JSON object"}}})
		return nil, false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"$", "must contain one JSON object"}}})
		return nil, false
	}
	return object, true
}
func revisionField(obj map[string]json.RawMessage) (int64, []problem) {
	var revision int64
	if json.Unmarshal(obj["revision"], &revision) != nil || revision < 1 {
		return 0, []problem{{"revision", "must be a positive integer"}}
	}
	return revision, nil
}
func unknownFields(obj map[string]json.RawMessage, allowed ...string) []problem {
	known := map[string]bool{}
	for _, key := range allowed {
		known[key] = true
	}
	var keys []string
	for key := range obj {
		if !known[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var out []problem
	for _, key := range keys {
		out = append(out, problem{key, "unknown field"})
	}
	return out
}
func textField(obj map[string]json.RawMessage, key string) (string, []problem) {
	var text string
	if json.Unmarshal(obj[key], &text) != nil {
		return "", []problem{{key, "must be a string"}}
	}
	out := problemList(config.ReaderNotesProblems(text))
	for i := range out {
		out[i].Path = key
	}
	return text, out
}
func (s *Server) reviewDraft(w http.ResponseWriter, r *http.Request) {
	date, ok := draftDate(w, r)
	if !ok {
		return
	}
	obj, ok := objectRequest(w, r)
	if !ok {
		return
	}
	revision, problems := revisionField(obj)
	var action string
	_ = json.Unmarshal(obj["action"], &action)
	allowed := []string{"revision", "action"}
	switch action {
	case "approve":
	case "cut":
		allowed = append(allowed, "item")
	case "swap":
		allowed = append(allowed, "item", "with")
	case "edit":
		allowed = append(allowed, "item", "fields", "kind", "title", "summary", "why", "source", "byline", "sources", "url", "minutes", "image_url")
	case "note":
		allowed = append(allowed, "text")
	case "send_back":
		allowed = append(allowed, "request")
	default:
		problems = append(problems, problem{"action", "must be one of approve, cut, swap, edit, note, send_back"})
	}
	problems = append(problems, unknownFields(obj, allowed...)...)
	note := ""
	if action == "note" || action == "send_back" {
		key := "text"
		if action == "send_back" {
			key = "request"
		}
		var errs []problem
		note, errs = textField(obj, key)
		problems = append(problems, errs...)
	}
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	p := current(r)
	d, err := s.Store.Draft(r.Context(), p.scope, date)
	if err != nil {
		draftError(w, err)
		return
	}
	if d.Revision != revision || d.Body == nil || d.State == "published" {
		draftError(w, store.ErrDraftConflict)
		return
	}
	if action == "cut" || action == "swap" || action == "edit" {
		problems = reviewBody(d.Body, obj, action)
		if len(problems) == 0 {
			problems, _ = validation(d.Body, p.paper)
		}
		if len(problems) > 0 {
			jsonResponse(w, 422, map[string]any{"errors": problems})
			return
		}
		raw, _ := json.Marshal(obj)
		note = string(raw)
	}
	rev, err := s.Store.ReviewDraft(r.Context(), p.scope, date, revision, p.keyID, action, note, d.Body)
	if err != nil {
		draftError(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"revision": rev})
}
func reviewBody(ed *edition.Edition, obj map[string]json.RawMessage, action string) []problem {
	var id string
	if json.Unmarshal(obj["item"], &id) != nil || id == "" {
		return []problem{{"item", "must be an existing item id"}}
	}
	for i := range ed.Sections {
		for j := range ed.Sections[i].Items {
			item := &ed.Sections[i].Items[j]
			if item.ID != id {
				continue
			}
			switch action {
			case "cut":
				ed.Sections[i].Items = append(ed.Sections[i].Items[:j], ed.Sections[i].Items[j+1:]...)
				if ed.Lead == id {
					ed.Lead = ""
					for _, sec := range ed.Sections {
						if len(sec.Items) > 0 {
							ed.Lead = sec.Items[0].ID
							break
						}
					}
				}
			case "swap":
				replacement, problems := decodeReviewItem(obj["with"], "with", ed.Schema)
				if len(problems) > 0 {
					return problems
				}
				*item = *replacement
				if ed.Lead == id {
					ed.Lead = replacement.ID
				}
			case "edit":
				patch := map[string]json.RawMessage{}
				for key, value := range obj {
					if key != "revision" && key != "action" && key != "item" && key != "fields" {
						patch[key] = value
					}
				}
				if raw, ok := obj["fields"]; ok {
					if len(patch) > 0 {
						return []problem{{"fields", "must not be combined with top-level item fields"}}
					}
					if json.Unmarshal(raw, &patch) != nil || patch == nil {
						return []problem{{"fields", "must be an object of item fields"}}
					}
					if problems := unknownFields(patch, "kind", "title", "summary", "why", "source", "byline", "sources", "url", "minutes", "image_url"); len(problems) > 0 {
						for i := range problems {
							problems[i].Path = "fields." + problems[i].Path
						}
						return problems
					}
				}
				if len(patch) == 0 {
					return []problem{{"fields", "must contain at least one item field"}}
				}
				raw, _ := json.Marshal(item)
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				for key, value := range patch {
					fields[key] = value
				}
				raw, _ = json.Marshal(fields)
				path := ""
				if _, nested := obj["fields"]; nested {
					path = "fields"
				}
				replacement, problems := decodeReviewItem(raw, path, ed.Schema)
				if len(problems) > 0 {
					return problems
				}
				*item = *replacement
			}
			return nil
		}
	}
	return []problem{{"item", "must be an existing item id"}}
}

// decodeReviewItem reuses the edition shape validator while reporting request paths.
func decodeReviewItem(raw json.RawMessage, path string, schema int) (*edition.Item, []problem) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, []problem{{path, "must be an item object"}}
	}
	itemType := reflect.TypeFor[edition.Item]()
	allowed := make([]string, 0, itemType.NumField())
	for i := 0; i < itemType.NumField(); i++ {
		field := itemType.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		allowed = append(allowed, name)
		if value, present := object[name]; present {
			kind := field.Type.Kind()
			if kind == reflect.Pointer {
				kind = field.Type.Elem().Kind()
			}
			if (kind == reflect.String || kind == reflect.Int) && json.Unmarshal(value, reflect.New(field.Type).Interface()) != nil {
				rule := "must be a string"
				if kind == reflect.Int {
					rule = "must be an integer"
				}
				if path != "" {
					name = path + "." + name
				}
				return nil, []problem{{name, rule}}
			}
		}
	}
	if problems := unknownFields(object, allowed...); len(problems) > 0 {
		for i := range problems {
			if path != "" {
				problems[i].Path = path + "." + problems[i].Path
			}
		}
		return nil, problems
	}
	wrapper := []byte(fmt.Sprintf(`{"schema":%d,"lead":"x","sections":[{"key":"x","items":[%s]}]}`, schema, raw))
	decoded, err := edition.Decode(bytes.NewReader(wrapper))
	if err != nil {
		problems := problemList([]string{err.Error()})
		for i := range problems {
			field := strings.TrimPrefix(strings.TrimPrefix(problems[i].Path, "sections[0].items[0]"), ".")
			problems[i].Path = field
			if path != "" {
				if field == "" {
					problems[i].Path = path
				} else {
					problems[i].Path = path + "." + field
				}
			}
		}
		return nil, problems
	}
	return &decoded.Sections[0].Items[0], nil
}

func (s *Server) publishDraft(w http.ResponseWriter, r *http.Request) {
	date, ok := draftDate(w, r)
	if !ok {
		return
	}
	obj, ok := objectRequest(w, r)
	if !ok {
		return
	}
	revision, problems := revisionField(obj)
	problems = append(problems, unknownFields(obj, "revision")...)
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	p := current(r)
	d, err := s.Store.Draft(r.Context(), p.scope, date)
	if err != nil {
		draftError(w, err)
		return
	}
	if d.Revision != revision || d.State == "published" || (p.paper.Role() != "author" && (d.ApprovedRevision == nil || *d.ApprovedRevision != revision)) {
		draftError(w, store.ErrDraftConflict)
		return
	}
	problems, warnings := validation(d.Body, p.paper)
	problems = append(problems, cutOffProblems(d.Body)...)
	sourceWarnings, checks := s.publishSourceChecks(r.Context(), p.scope, p.paper, d.Body)
	warnings = append(warnings, sourceWarnings...)
	if len(problems) > 0 {
		if err = s.Store.RecordPublishDiagnostics(r.Context(), p.paper.ID, date, publishDiagnostics(p.paper, "draft", warnings, problems, checks)); err != nil {
			s.storageFailed(w)
			return
		}
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	fetched, imageWarnings := s.retainImages(r.Context(), d.Body)
	warnings = append(warnings, imageWarnings...)
	rev, err := s.Store.PublishDraftWithDiagnostics(r.Context(), p.scope, date, revision, p.paper.Role() == "author", p.keyID, fetched, publishDiagnostics(p.paper, "draft", warnings, problems, checks), s.layoutFor(p.paper))
	if err != nil {
		draftError(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"revision": rev, "date": date, "warnings": warnings})
}

// ApplyDeadline implements scheduler.PolicyApplier. Network image retention takes
// place before the store's revision/state/publication transaction.
func (s *Server) ApplyDeadline(ctx context.Context, p config.Paper, date string) error {
	published, err := s.Store.RunPublished(ctx, p.ID, date)
	if err != nil {
		return err
	}
	if published {
		return s.Store.FinishRun(ctx, p.ID, date, "published")
	}
	d, err := s.Store.RunDraft(ctx, p.ID, date)
	if errors.Is(err, sql.ErrNoRows) || err == nil && d.Body == nil && d.State == "gathering" {
		return s.Store.NoDeadlineDraft(ctx, p.ID, date)
	}
	if err != nil {
		return err
	}
	if d.State == "held" || d.State == "published" {
		return s.Store.FinishRun(ctx, p.ID, date, d.State)
	}
	missedReview := p.Role() != "none" && (d.ApprovedRevision == nil || *d.ApprovedRevision != d.Revision)
	hold := (missedReview && p.Policy() == "hold") || d.Body == nil
	var problems, warnings []problem
	var checks []store.SourceCheckCount
	if d.Body != nil {
		problems, warnings = validation(d.Body, p)
		problems = append(problems, cutOffProblems(d.Body)...)
		hold = hold || len(problems) > 0
	} else {
		problems = []problem{{"$", "no draft body"}}
	}
	var fetched map[string]store.Image
	if !hold {
		sourceWarnings, outcomes := sourceWarningsWithChecks(p, d.Body, func(link string) (string, bool, error) {
			return s.Store.RunSnapshotText(ctx, p.ID, link)
		})
		checks = outcomes
		warnings = append(warnings, sourceWarnings...)
		// Scheduled publication has no HTTP response; report counts only, never text.
		if len(warnings) > 0 && s.Log != nil {
			s.Log.Warn("deadline source check warnings", "paper", p.ID, "date", date, "count", len(warnings))
		}
		var imageWarnings []problem
		fetched, imageWarnings = s.retainImages(ctx, d.Body)
		warnings = append(warnings, imageWarnings...)
	}
	return s.Store.ApplyDeadlinePolicyWithDiagnostics(ctx, p.ID, date, d.Revision, hold, missedReview, fetched, publishDiagnostics(p, "deadline", warnings, problems, checks), s.layoutFor(p))
}
func (s *Server) getNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := s.Store.ReaderNotes(r.Context(), current(r).scope)
	if err != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, notes)
}
func (s *Server) putNotes(w http.ResponseWriter, r *http.Request) {
	obj, ok := objectRequest(w, r)
	if !ok {
		return
	}
	text, problems := textField(obj, "text")
	problems = append(problems, unknownFields(obj, "text")...)
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	notes, err := s.Store.SaveReaderNotes(r.Context(), current(r).scope, text)
	if err != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, notes)
}

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/webhook"
)

const testKey = "synthetic-conformance-test-key"

type referenceClient struct {
	t   *testing.T
	url string
}

func (c referenceClient) call(method, path string, body any, want int) []byte {
	c.t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, c.url+path, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, want, data)
	}
	// Decode successful fixtures into the very response types used to generate
	// OpenAPI, rejecting extra fields and incorrect JSON shapes.
	if want >= 200 && want < 300 {
		for _, route := range api.Routes() {
			if route.Method == method && strings.ReplaceAll(route.Path, "{date}", Date) == path && route.Response != nil {
				dec := json.NewDecoder(bytes.NewReader(data))
				dec.DisallowUnknownFields()
				if err := dec.Decode(reflect.New(route.Response).Interface()); err != nil {
					c.t.Fatalf("fixture differs from route response type: %v", err)
				}
			}
		}
	}
	return data
}
func newKit(t *testing.T, scenario string) (*Server, referenceClient) {
	t.Helper()
	s, err := New(scenario, []byte(testKey))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	return s, referenceClient{t, server.URL}
}
func assertReport(t *testing.T, s *Server, c referenceClient, passed bool) {
	t.Helper()
	var report Report
	if err := json.Unmarshal(c.call("GET", "/conformance/report", nil, 200), &report); err != nil {
		t.Fatal(err)
	}
	if report.Passed != passed {
		t.Fatalf("report = %+v", report)
	}
	if passed {
		select {
		case <-s.Done():
		default:
			t.Fatal("completed report did not signal Done")
		}
	}
}

// This client deliberately lives in the test: none of the scenario machinery
// acts as the integration under test. Both direct publication and submission
// are exercised where the protocol permits them.
func TestEveryScenarioWithReferenceClient(t *testing.T) {
	for _, scenario := range Scenarios() {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, c := newKit(t, scenario)
			c.call("GET", "/api/v1/paper", nil, 200)
			path := "/api/v1/editions/" + Date
			switch scenario {
			case "validation":
				body := Fixture()
				body.Sections[0].Items[0].URL = "http://example.com/library"
				var problem struct {
					Errors []struct{ Path, Rule string } `json:"errors"`
				}
				if err := json.Unmarshal(c.call("PUT", path, body, 422), &problem); err != nil {
					t.Fatal(err)
				}
				if len(problem.Errors) != 1 || problem.Errors[0].Path != "sections[0].items[0].url" || problem.Errors[0].Rule != "must be https" {
					t.Fatalf("exact validation: %+v", problem)
				}
				body.Sections[0].Items[0].URL = "https://example.com/library"
				c.call("PUT", path, body, 200)
			case "retry":
				for i, status := range []int{503, 429, 200} {
					body, _ := json.Marshal(Fixture())
					req, _ := http.NewRequest("PUT", c.url+path, bytes.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+testKey)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
					if resp.StatusCode != status {
						t.Fatalf("attempt %d: %d", i, resp.StatusCode)
					}
					if status == 503 {
						time.Sleep(110 * time.Millisecond)
					}
					if status == 429 {
						delay, err := strconv.Atoi(resp.Header.Get("Retry-After"))
						if err != nil || delay != 1 {
							t.Fatal("missing Retry-After: 1")
						}
						time.Sleep(time.Duration(delay) * time.Second)
					}
				}
				// Repeating the successful PUT is safe and does not create another identity.
				c.call("PUT", path, Fixture(), 200)
				time.Sleep(QuietPeriod)
			case "review":
				base := "/api/v1/drafts/" + Date
				var draft store.Draft
				if err := json.Unmarshal(c.call("GET", base, nil, 200), &draft); err != nil {
					t.Fatal(err)
				}
				if draft.State != "in_review" || draft.Body == nil {
					t.Fatalf("draft: %+v", draft)
				}
				c.call("POST", base+"/review", map[string]any{"revision": draft.Revision, "action": "approve"}, 409)
				if err := json.Unmarshal(c.call("GET", base, nil, 200), &draft); err != nil {
					t.Fatal(err)
				}
				var approved struct {
					Revision int64 `json:"revision"`
				}
				if err := json.Unmarshal(c.call("POST", base+"/review", map[string]any{"revision": draft.Revision, "action": "approve"}, 200), &approved); err != nil {
					t.Fatal(err)
				}
				c.call("POST", base+"/publish", map[string]any{"revision": approved.Revision}, 200)
			case "forbidden":
				data := c.call("PUT", path, Fixture(), 403)
				if !strings.Contains(string(data), `"error":"forbidden"`) {
					t.Fatalf("403 body: %s", data)
				}
				assertReport(t, s, c, false) // The quiet period must actually elapse.
				time.Sleep(QuietPeriod)
			case "webhooks":
				received := []string{}
				var receivedMu sync.Mutex
				receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, _ := io.ReadAll(r.Body)
					timestamp, _ := strconv.ParseInt(r.Header.Get("Night-Desk-Timestamp"), 10, 64)
					if err := webhook.Verify([]byte(testKey), r.Header.Get("Night-Desk-Signature-256"), timestamp, data, time.Now(), 5*time.Minute); err != nil {
						w.WriteHeader(401)
						return
					}
					var e webhook.Event
					if json.Unmarshal(data, &e) != nil || e.ID != r.Header.Get("Night-Desk-Delivery") || e.Type != r.Header.Get("Night-Desk-Event") || r.Method != "POST" {
						w.WriteHeader(400)
						return
					}
					receivedMu.Lock()
					received = append(received, e.Type)
					receivedMu.Unlock()
					w.WriteHeader(204)
				}))
				defer receiver.Close()
				s.DeliverWebhooks(context.Background(), receiver.URL)
				receivedMu.Lock()
				defer receivedMu.Unlock()
				if strings.Join(received, ",") != "draft.ready,review.requested" {
					t.Fatalf("events: %v", received)
				}
			}
			assertReport(t, s, c, true)
		})
	}
}

func TestSubmissionValidation(t *testing.T) {
	s, c := newKit(t, "validation")
	body := Fixture()
	body.Sections[0].Items[0].URL = "http://example.com/library"
	path := "/api/v1/drafts/" + Date
	c.call("PUT", path, body, 422)
	c.call("PUT", path, Fixture(), 200)
	assertReport(t, s, c, true)
}
func TestFailedClients(t *testing.T) {
	t.Run("uncorrected body", func(t *testing.T) {
		s, c := newKit(t, "validation")
		path := "/api/v1/editions/" + Date
		c.call("PUT", path, Fixture(), 422)
		c.call("PUT", path, Fixture(), 200)
		assertReport(t, s, c, false)
	})
	t.Run("retry timing and changed content", func(t *testing.T) {
		s, c := newKit(t, "retry")
		path := "/api/v1/editions/" + Date
		c.call("PUT", path, Fixture(), 503)
		body := Fixture()
		body.Sections[0].Items[0].Title = "A different edition"
		c.call("PUT", path, body, 429)
		c.call("PUT", path, body, 200)
		time.Sleep(QuietPeriod)
		assertReport(t, s, c, false)
		for _, check := range s.Report().Checks {
			if check.Name != "retry_sequence" && check.Passed {
				t.Fatalf("bad client passed %s", check.Name)
			}
		}
	})
	t.Run("retry forbidden", func(t *testing.T) {
		s, c := newKit(t, "forbidden")
		path := "/api/v1/editions/" + Date
		c.call("PUT", path, Fixture(), 403)
		c.call("PUT", path, Fixture(), 403)
		time.Sleep(QuietPeriod)
		assertReport(t, s, c, false)
	})
	t.Run("stale approval", func(t *testing.T) {
		s, c := newKit(t, "review")
		base := "/api/v1/drafts/" + Date
		c.call("POST", base+"/review", map[string]any{"revision": 1, "action": "approve"}, 409) // Must read first.
		c.call("GET", base, nil, 200)
		c.call("POST", base+"/review", map[string]any{"revision": 1, "action": "approve"}, 409)
		c.call("POST", base+"/review", map[string]any{"revision": 2, "action": "approve"}, 409) // Must reread.
		c.call("POST", base+"/publish", map[string]any{"revision": 2}, 409)
		assertReport(t, s, c, false)
	})
	t.Run("accept all webhooks", func(t *testing.T) {
		s, c := newKit(t, "webhooks")
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
		defer receiver.Close()
		s.DeliverWebhooks(context.Background(), receiver.URL)
		assertReport(t, s, c, false)
	})
}
func TestSecurityAndIncompleteReports(t *testing.T) {
	if _, err := New("unknown", []byte(testKey)); err == nil {
		t.Fatal("accepted unknown scenario")
	}
	for _, key := range []string{"", "contains space", "a\nb", "é"} {
		if _, err := New("validation", []byte(key)); err == nil {
			t.Fatal("accepted invalid key")
		}
	}
	for _, scenario := range Scenarios() {
		s, c := newKit(t, scenario)
		assertReport(t, s, c, false)
	}
	s, _ := newKit(t, "validation")
	for _, remote := range []string{"192.0.2.1:1000", "invalid"} {
		req := httptest.NewRequest("GET", "/conformance/report", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatal("non-loopback report allowed")
		}
	}
	req := httptest.NewRequest("GET", "/conformance/report", nil)
	req.RemoteAddr = "[::1]:1000"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("loopback IPv6 denied")
	}
	req = httptest.NewRequest("PUT", "/api/v1/editions/"+Date, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong-synthetic-key")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 401 || s.attempts != 0 {
		t.Fatal("unauthorized request advanced script")
	}
}

func TestStrictValidationPaths(t *testing.T) {
	_, c := newKit(t, "review")
	for _, tt := range []struct {
		body any
		path string
	}{
		{map[string]any{"revision": 1, "action": "approve", "extra": true}, "extra"},
		{map[string]any{"revision": "1", "action": "approve"}, "revision"},
		{map[string]any{"revision": 1, "action": "unknown"}, "action"},
		{nil, "$"},
	} {
		var problem struct {
			Errors []struct{ Path, Rule string } `json:"errors"`
		}
		if err := json.Unmarshal(c.call("POST", "/api/v1/drafts/"+Date+"/review", tt.body, 422), &problem); err != nil {
			t.Fatal(err)
		}
		if len(problem.Errors) != 1 || problem.Errors[0].Path != tt.path || problem.Errors[0].Rule == "" {
			t.Fatalf("validation: %+v", problem)
		}
	}
	_, c = newKit(t, "validation")
	path := "/api/v1/editions/" + Date
	c.call("PUT", path, Fixture(), 422)
	raw, _ := json.Marshal(Fixture())
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	object["sections"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)["title"] = 42
	data := c.call("PUT", path, object, 422)
	if !strings.Contains(string(data), `"path":"sections[0].items[0].title","rule":"must be a string"`) {
		t.Fatalf("shape validation: %s", data)
	}
}

func TestWebhookRedirectsFailWithoutFollowing(t *testing.T) {
	s, c := newKit(t, "webhooks")
	followed := 0
	var followedMu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followedMu.Lock()
		followed++
		followedMu.Unlock()
		w.WriteHeader(204)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	s.DeliverWebhooks(context.Background(), redirect.URL)
	assertReport(t, s, c, false)
	followedMu.Lock()
	defer followedMu.Unlock()
	if followed != 0 {
		t.Fatal("followed webhook redirect")
	}
}

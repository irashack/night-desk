package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/store"
)

// maxHealthHosts matches the worker's per-run bound.
const maxHealthHosts = 500

// postHealth records counts and host names only; never URLs or article text.
func (s *Server) postHealth(w http.ResponseWriter, r *http.Request) {
	obj, ok := objectRequest(w, r)
	if !ok {
		return
	}
	problems := unknownFields(obj, "hosts")
	var raw []map[string]json.RawMessage
	if json.Unmarshal(obj["hosts"], &raw) != nil || raw == nil {
		problems = append(problems, problem{"hosts", "must be an array of host counts"})
	} else if len(raw) > maxHealthHosts {
		problems = append(problems, problem{"hosts", fmt.Sprintf("must contain at most %d hosts", maxHealthHosts)})
	}
	hosts := make([]store.HostHealth, len(raw))
	seen := map[string]bool{}
	for i, item := range raw {
		base := fmt.Sprintf("hosts[%d].", i)
		var h store.HostHealth
		fields := []struct {
			name string
			dst  *int
		}{{"ok", &h.OK}, {"failed_http_4xx", &h.FailedHTTP4xx}, {"failed_http_429", &h.FailedHTTP429}, {"failed_http_5xx", &h.FailedHTTP5xx}, {"failed_network", &h.FailedNetwork}, {"failed_too_large", &h.FailedTooLarge}, {"cooled_off", &h.CooledOff}, {"readability", &h.Readability}, {"json_ld", &h.JSONLD}, {"amp", &h.AMP}}
		allowed := []string{"host"}
		for _, f := range fields {
			allowed = append(allowed, f.name)
		}
		for _, p := range unknownFields(item, allowed...) {
			problems = append(problems, problem{base + p.Path, p.Rule})
		}
		if json.Unmarshal(item["host"], &h.Host) != nil || !hostName(h.Host) {
			problems = append(problems, problem{base + "host", "must be a lowercase host name"})
		} else if seen[h.Host] {
			problems = append(problems, problem{base + "host", "must be unique"})
		}
		seen[h.Host] = true
		for _, f := range fields {
			if v, present := item[f.name]; present && (json.Unmarshal(v, f.dst) != nil || *f.dst < 0 || *f.dst > 100000) {
				problems = append(problems, problem{base + f.name, "must be an integer between 0 and 100000"})
			}
		}
		hosts[i] = h
	}
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	p := current(r)
	loc, err := time.LoadLocation(p.paper.Timezone)
	if err != nil {
		s.storageFailed(w)
		return
	}
	if err = s.Store.AddFetchHealth(r.Context(), p.scope, time.Now().In(loc).Format("2006-01-02"), hosts); err != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, acceptedResponse{Accepted: len(hosts)})
}

// hostName accepts a lowercase DNS name or IP literal, as the worker reports.
func hostName(h string) bool {
	if h == "" || len(h) > 253 || h != strings.ToLower(h) {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

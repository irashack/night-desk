// Package operator serves operational counts with a credential independent of
// reader sessions and paper-scoped agent keys.
package operator

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/quality"
	"github.com/irashack/night-desk/internal/store"
)

// New loads a SHA-256 hex digest at startup. An absent file setting disables
// the page; a configured unreadable or invalid digest fails startup closed.
func New(db *store.Store, current func() *config.Config) (http.Handler, error) {
	path := current().Server.Operator.KeyHashFile
	if path == "" {
		return http.NotFoundHandler(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("server.operator.key_hash_file: must name a readable SHA-256 hash file")
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("server.operator.key_hash_file: must contain one SHA-256 hex digest")
	}
	t, err := template.New("operator").Parse(page)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /operator", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Vary", "Authorization")
		w.Header().Set("Referrer-Policy", "no-referrer")
		key := ""
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) == 2 && fields[0] == "Bearer" {
			key = fields[1]
		}
		if user, password, ok := r.BasicAuth(); ok && user == "operator" {
			key = password
		}
		digest := sha256.Sum256([]byte(key))
		if key == "" || subtle.ConstantTimeCompare(digest[:], decoded) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Night Desk operator", charset="UTF-8"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		days := 14
		if values, ok := r.URL.Query()["days"]; ok {
			if len(values) != 1 {
				http.Error(w, "days: must be between 1 and 3660", 400)
				return
			}
			var parseErr error
			days, parseErr = strconv.Atoi(values[0])
			if parseErr != nil || days < 1 || days > 3660 {
				http.Error(w, "days: must be between 1 and 3660", 400)
				return
			}
		}
		report, err := quality.Build(r.Context(), db, current().Papers, days, time.Now())
		if err != nil {
			http.Error(w, "Quality report unavailable", 500)
			return
		}
		var body bytes.Buffer
		if err := t.Execute(&body, report); err != nil {
			http.Error(w, "Quality report unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body.Bytes())
	})
	return mux, nil
}

// This template accepts only the shared aggregate report. It deliberately does
// not render the legacy CLI feedback trend, claim values, or edition content.
const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Night Desk operator</title><link rel="stylesheet" href="/static/app.css"></head>
<body><main class="reader"><h1>Night Desk quality</h1>
<form class="search-form" method="get" action="/operator"><label>Window in days <input name="days" type="number" min="1" max="3660" value="{{if .Papers}}{{(index .Papers 0).Days}}{{else}}14{{end}}"></label><button type="submit">Show counts</button></form>
{{range .Papers}}<section class="page"><h2>{{.PaperID}}</h2><p>{{.Start}} to {{.End}}</p>
<dl><dt>Editions published</dt><dd>{{.Published}}</dd><dt>Unreviewed</dt><dd>{{.Unreviewed}}</dd><dt>Held</dt><dd>{{.Held}}</dd><dt>Drafts sent back</dt><dd>{{.DraftsSentBack}}</dd><dt>Deadlines met / published checked</dt><dd>{{.DeadlinesMet}} / {{.DeadlinesChecked}}</dd><dt>Deadline misses</dt><dd>{{.DeadlineMisses}}</dd><dt>Empty required sections</dt><dd>{{.EmptyRequiredSections}}</dd><dt>Drafting runs / tokens</dt><dd>{{.DraftRuns}} / {{.DraftTokens}}</dd><dt>Estimated drafting cost</dt><dd>${{printf "%.2f" .DraftCostUSD}}</dd><dt>Review turnaround median (minutes)</dt><dd>{{if .ReviewTurnaroundSamples}}{{printf "%.1f" .ReviewTurnaroundMedianMinutes}}{{else}}No samples{{end}}</dd></dl>
<h3>Warnings by rule</h3><ul>{{range $rule,$count:=.WarningsByRule}}<li>{{$rule}}: {{$count}}</li>{{else}}<li>None</li>{{end}}</ul>
<h3>Failed source checks by rule</h3><ul>{{range $rule,$count:=.FailedSourceChecksByRule}}<li>{{$rule}}: {{$count}}</li>{{else}}<li>None</li>{{end}}</ul>
<h3>Unverified source checks by rule</h3><ul>{{range $rule,$count:=.UnverifiedSourceChecksByRule}}<li>{{$rule}}: {{$count}}</li>{{else}}<li>None</li>{{end}}</ul>
<h3>Gathered items dropped by filter</h3><ul>{{range $rule,$count:=.DroppedByRule}}<li>{{$rule}}: {{$count}}</li>{{else}}<li>None</li>{{end}}</ul>
<h3>Article fetch health</h3>{{if .FetchHealth}}<table><thead><tr><th>Host</th><th>OK</th><th>Failed</th><th>4xx</th><th>429</th><th>5xx</th><th>Network</th><th>Too large</th><th>Cooled off</th><th>Readability</th><th>JSON-LD</th><th>AMP</th></tr></thead><tbody>{{range .FetchHealth}}<tr><td>{{.Host}}</td><td>{{.OK}}</td><td>{{.Failed}}</td><td>{{.FailedHTTP4xx}}</td><td>{{.FailedHTTP429}}</td><td>{{.FailedHTTP5xx}}</td><td>{{.FailedNetwork}}</td><td>{{.FailedTooLarge}}</td><td>{{.CooledOff}}</td><td>{{.Readability}}</td><td>{{.JSONLD}}</td><td>{{.AMP}}</td></tr>{{end}}</tbody></table>{{else}}<p>No fetches reported.</p>{{end}}
</section>{{end}}</main></body></html>`

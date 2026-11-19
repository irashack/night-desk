package reader

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/store"
)

// subscription is the browser's PushSubscription.toJSON(). The p256dh and auth
// keys are ignored: pushes carry no payload, so nothing is encrypted to them.
type subscription struct {
	Endpoint       string   `json:"endpoint"`
	ExpirationTime *float64 `json:"expirationTime"`
}

// pushForm reads the CSRF-checked form field "subscription". It writes the
// error response itself and reports whether the caller may continue.
func (s *Server) pushForm(w http.ResponseWriter, r *http.Request) (subscription, bool) {
	var sub subscription
	if s.Push == nil {
		http.NotFound(w, r)
		return sub, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid form", 400)
		return sub, false
	}
	if !validCSRF(r) {
		http.Error(w, "Invalid CSRF token", 403)
		return sub, false
	}
	if json.Unmarshal([]byte(r.PostForm.Get("subscription")), &sub) != nil {
		http.Error(w, "Invalid subscription", 400)
		return sub, false
	}
	if err := push.CheckEndpoint(sub.Endpoint); err != nil {
		http.Error(w, "Invalid subscription", 400)
		return sub, false
	}
	return sub, true
}
func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.pushForm(w, r)
	if !ok {
		return
	}
	var expires *time.Time
	if sub.ExpirationTime != nil && *sub.ExpirationTime > 0 {
		at := time.UnixMilli(int64(*sub.ExpirationTime))
		if !at.After(s.now()) {
			http.Error(w, "Invalid subscription", 400)
			return
		}
		expires = &at
	}
	err := s.Store.AddPushSubscription(r.Context(), current(r).scope, sub.Endpoint, expires)
	if errors.Is(err, store.ErrPushLimit) {
		http.Error(w, "Too many devices", 409)
		return
	}
	if err != nil {
		http.Error(w, "Notifications unavailable", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.pushForm(w, r)
	if !ok {
		return
	}
	if err := s.Store.RemovePushSubscription(r.Context(), current(r).scope, sub.Endpoint); err != nil {
		http.Error(w, "Notifications unavailable", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

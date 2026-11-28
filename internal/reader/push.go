package reader

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/store"
)

// subscription is the browser's PushSubscription.toJSON().
type subscription struct {
	Keys           json.RawMessage `json:"keys"`
	Endpoint       string          `json:"endpoint"`
	ExpirationTime *float64        `json:"expirationTime"`
}

func (sub subscription) keys() (store.PushKeys, bool) {
	if sub.Keys == nil {
		return store.PushKeys{}, true
	}
	var keys store.PushKeys
	if json.Unmarshal(sub.Keys, &keys) != nil || !push.ValidKeys(keys.P256DH, keys.Auth) {
		return store.PushKeys{}, false
	}
	return keys, true
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
	keys, valid := sub.keys()
	if !valid {
		http.Error(w, "Invalid subscription", 400)
		return
	}
	err := s.Store.AddPushSubscription(r.Context(), current(r).scope, sub.Endpoint, expires, keys)
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
	remaining, err := s.Store.RemovePushSubscription(r.Context(), current(r).scope, sub.Endpoint)
	if err != nil {
		http.Error(w, "Notifications unavailable", 500)
		return
	}
	// The browser keeps one subscription for every paper; it may drop it only
	// when no other paper still notifies through it.
	w.Header().Set("X-Push-Remaining", strconv.Itoa(remaining))
	w.WriteHeader(http.StatusNoContent)
}

// pushStatus tells the toggle whether this device notifies for this paper.
func (s *Server) pushStatus(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.pushForm(w, r)
	if !ok {
		return
	}
	keys, _ := sub.keys()
	on, err := s.Store.HasPushSubscription(r.Context(), current(r).scope, sub.Endpoint, keys)
	if err != nil {
		http.Error(w, "Notifications unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"on": on})
}

// pushOpen is where a notification click lands (#34): the paper that most
// recently announced an edition this reader has notifications on for, which
// may be a group paper rather than the default one.
func (s *Server) pushOpen(w http.ResponseWriter, r *http.Request) {
	scope := current(r).scope
	target := "/"
	paper, ok, err := s.Store.LatestNotifiedPaper(r.Context(), scope)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	if papers := scope.Papers(); ok && len(papers) > 0 && paper != papers[0].Paper {
		target = paperBase(paper) + "/"
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

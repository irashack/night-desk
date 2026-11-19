package api

import (
	"net/http"

	"github.com/irashack/night-desk/internal/auth"
)

func (s *Server) require(c auth.Capability, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !current(r).scope.Can(c) {
			fail(w, http.StatusForbidden, "forbidden")
			return
		}
		h(w, r)
	}
}

// requireAny allows the request when the key holds at least one of cs.
func (s *Server) requireAny(h http.HandlerFunc, cs ...auth.Capability) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, c := range cs {
			if current(r).scope.Can(c) {
				h(w, r)
				return
			}
		}
		fail(w, http.StatusForbidden, "forbidden")
	}
}

package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	clientID     = "night-desk-demo"
	clientSecret = "demo-client-secret"
)

// startProvider serves a minimal OpenID Connect provider on loopback that
// signs in every visitor as the demo reader without asking anything. It
// still checks PKCE and returns a signed ID token with the reader's nonce, so
// the reader's real sign-in code path runs unchanged.
func startProvider(ctx context.Context) (string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	issuer := "http://" + listener.Addr().String()
	var mu sync.Mutex
	codes := map[string][2]string{} // code -> nonce, PKCE challenge
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "demo", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		dest, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || q.Get("client_id") != clientID || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad authorization request", http.StatusBadRequest)
			return
		}
		code := random()
		mu.Lock()
		codes[code] = [2]string{q.Get("nonce"), q.Get("code_challenge")}
		mu.Unlock()
		dest.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, dest.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		pair, ok := codes[r.FormValue("code")]
		delete(codes, r.FormValue("code"))
		mu.Unlock()
		challenge := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(challenge[:]) != pair[1] {
			http.Error(w, "bad code or verifier", http.StatusBadRequest)
			return
		}
		now := time.Now()
		claims, _ := json.Marshal(map[string]any{"iss": issuer, "sub": subject, "aud": clientID, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": pair[0]})
		unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"demo","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(unsigned))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			http.Error(w, "signing failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"access_token": random(), "token_type": "Bearer", "expires_in": 3600, "id_token": unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	go func() { <-ctx.Done(); _ = server.Close() }()
	return issuer, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func random() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

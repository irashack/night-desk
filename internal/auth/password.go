package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password hashing: argon2id with OWASP's recommended minimum (19 MiB, two
// passes, one lane), stored in the PHC string format so the parameters can be
// raised later without invalidating existing hashes.
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
)

// MinPasswordLength and MaxPasswordLength bound new passwords, in characters
// and bytes respectively.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 1024
)

var ErrWeakPassword = fmt.Errorf("a password needs at least %d characters", MinPasswordLength)

// CheckNewPassword applies the rules for a new password.
func CheckNewPassword(password string) error {
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("a password may be at most %d bytes", MaxPasswordLength)
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrWeakPassword
	}
	return nil
}

// hashing bounds concurrent argon2 computations: each takes 19 MiB, and a
// burst of sign-in attempts must not exhaust memory on a small machine.
var hashing = make(chan struct{}, 2)

func derive(password string, salt []byte, memory uint32, passes uint32, threads uint8, keyLen uint32) []byte {
	hashing <- struct{}{}
	defer func() { <-hashing }()
	return argon2.IDKey([]byte(password), salt, passes, memory, threads, keyLen)
}

// HashPassword returns the PHC-format argon2id hash of password.
func HashPassword(password string) (string, error) {
	if len(password) > MaxPasswordLength {
		return "", errors.New("password too long")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := derive(password, salt, argonMemory, argonTime, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// dummyHash is verified against when an account does not exist, so a missing
// account takes as long to refuse as a wrong password.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("night-desk-no-such-account")
	return h
})

// VerifyPassword reports whether password matches hash. An empty hash (no such
// account) still costs one derivation and returns false.
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		hash = dummyHash()
		password += "\x00"
	}
	if len(password) > MaxPasswordLength+1 {
		return false
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil {
		return false
	}
	// Refuse parameters that would let a stored hash exhaust the machine.
	if memory < 8*1024 || memory > 256*1024 || passes < 1 || passes > 10 || threads < 1 || threads > 8 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := derive(password, salt, memory, passes, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Limiter throttles password and setup-code attempts. Each key (an account
// name, or one client address) may fail a few times freely; after that it
// waits a doubling delay, capped at fifteen minutes, before its next attempt.
// A success clears its key. It never locks an account out for good, and the
// delay, not a refusal, bounds online guessing.
type Limiter struct {
	mu   sync.Mutex
	keys map[string]*throttle
	now  func() time.Time
}

type throttle struct {
	failures int
	next     time.Time
	last     time.Time
}

const (
	freeFailures = 5
	maxDelay     = 15 * time.Minute
	limiterKeys  = 10000
)

func NewLimiter() *Limiter { return &Limiter{keys: map[string]*throttle{}, now: time.Now} }

// SetClock replaces the limiter's clock, for tests.
func (l *Limiter) SetClock(now func() time.Time) { l.now = now }

// Wait reports how long every key must still wait; zero allows the attempt.
func (l *Limiter) Wait(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, k := range keys {
		if t, ok := l.keys[k]; ok && t.next.After(now) {
			wait = max(wait, t.next.Sub(now))
		}
	}
	return wait
}

// Fail records a failed attempt against every key.
func (l *Limiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.keys) >= limiterKeys {
		for k, t := range l.keys {
			if now.Sub(t.last) > maxDelay || t.next.Before(now) && t.failures < freeFailures {
				delete(l.keys, k)
			}
		}
	}
	for _, k := range keys {
		t, ok := l.keys[k]
		if !ok {
			if len(l.keys) >= limiterKeys {
				// Full of recent failures: forget the oldest, so spraying
				// names cannot stop a targeted account being tracked.
				var oldest string
				for k2, t2 := range l.keys {
					if oldest == "" || t2.last.Before(l.keys[oldest].last) {
						oldest = k2
					}
				}
				delete(l.keys, oldest)
			}
			t = &throttle{}
			l.keys[k] = t
		}
		// A key quiet for longer than the longest delay starts over.
		if now.Sub(t.last) > 2*maxDelay {
			t.failures = 0
		}
		t.failures++
		t.last = now
		if t.failures > freeFailures {
			delay := time.Second << min(t.failures-freeFailures-1, 20)
			t.next = now.Add(min(delay, maxDelay))
		}
	}
}

// Succeed clears key.
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, key)
}

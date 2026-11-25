package auth

import (
	"context"
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
// waiting bounds the queue behind them; past it, or after maxHashWait, an
// attempt is refused as busy rather than queued.
var (
	hashing = make(chan struct{}, 2)
	waiting = make(chan struct{}, 32)
)

const maxHashWait = 10 * time.Second

// ErrBusy refuses a password check while too many are already running.
var ErrBusy = errors.New("too many sign-ins at once")

func derive(ctx context.Context, password string, salt []byte, memory uint32, passes uint32, threads uint8, keyLen uint32) ([]byte, error) {
	select {
	case waiting <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	timer := time.NewTimer(maxHashWait)
	defer timer.Stop()
	select {
	case hashing <- struct{}{}:
		<-waiting
	case <-ctx.Done():
		<-waiting
		return nil, ctx.Err()
	case <-timer.C:
		<-waiting
		return nil, ErrBusy
	}
	defer func() { <-hashing }()
	return argon2.IDKey([]byte(password), salt, passes, memory, threads, keyLen), nil
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
	key, err := derive(context.Background(), password, salt, argonMemory, argonTime, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// dummyHash is verified against when an account does not exist, so a missing
// account takes as long to refuse as a wrong password. It is a constant (no
// password matches it) so that it can never be missing.
func dummyHash() string {
	return "$argon2id$v=19$m=19456,t=2,p=1$bmlnaHQtZGVzay1kdW1teQ$FkZ0dXBnaHlhY2N0dW5vcm1hbHdheWZvcnRpbWluZ3M"
}

// VerifyPassword reports whether password matches hash. An empty hash (no such
// account) still costs one derivation and returns false. It returns ErrBusy,
// or the context's error, when the check could not run.
func VerifyPassword(ctx context.Context, hash, password string) (bool, error) {
	// Refuse an over-long password before anything that depends on the
	// account, so its length reveals nothing about whether the account exists.
	if len(password) > MaxPasswordLength {
		return false, nil
	}
	if hash == "" {
		hash = dummyHash()
		password += "\x00"
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, nil
	}
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, passes, threads) {
		return false, nil
	}
	// Refuse parameters that would let a stored hash exhaust the machine.
	if memory < 8*1024 || memory > 256*1024 || passes < 1 || passes > 10 || threads < 1 || threads > 8 {
		return false, nil
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false, nil
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, nil
	}
	got, err := derive(ctx, password, salt, memory, passes, threads, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
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

// Attempt asks to try once for every key. It returns how long the caller
// must still wait, or zero and counts the attempt as failed at once, so that
// parallel attempts cannot all pass before any failure is recorded; Succeed
// clears a key after a success.
func (l *Limiter) Attempt(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, k := range keys {
		if t, ok := l.keys[k]; ok && t.next.After(now) {
			wait = max(wait, t.next.Sub(now))
		}
	}
	if wait > 0 {
		return wait
	}
	l.fail(now, keys)
	return 0
}

func (l *Limiter) fail(now time.Time, keys []string) {
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

// Succeed clears keys.
func (l *Limiter) Succeed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.keys, k)
	}
}

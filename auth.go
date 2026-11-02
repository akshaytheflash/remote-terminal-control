package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The alphabet avoids characters people confuse when copying a code by hand
// (no I, L, O, U, 0 or 1). 32 symbols => 5 bits per character.
const otpAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	otpLength  = 8
	tokenTTL   = 24 * time.Hour
	cookieName = "rt_auth"
)

var (
	errBadPassword = errors.New("wrong password")
	errBurned      = errors.New("this password was already used - press p on the computer for a new one")
	errLocked      = errors.New("too many attempts")
)

// Auth holds the single-use password and the browser sessions created with it.
type Auth struct {
	mu       sync.Mutex
	otp      string // empty once used
	tokens   map[string]time.Time
	fails    int
	lockedTo time.Time
}

func NewAuth() *Auth {
	a := &Auth{tokens: make(map[string]time.Time)}
	a.otp = newOTP()
	return a
}

func newOTP() string {
	b := make([]byte, otpLength)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	var sb strings.Builder
	for _, c := range b {
		sb.WriteByte(otpAlphabet[int(c)%len(otpAlphabet)])
	}
	s := sb.String()
	return s[:4] + "-" + s[4:]
}

// normalizeOTP makes typed passwords forgiving: case and separators do not
// matter, so "k7qm 3xpd" and "K7QM-3XPD" are the same.
func normalizeOTP(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Password returns the current unused password ("" if it has been used).
func (a *Auth) Password() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.otp
}

// Rotate issues a fresh single-use password and clears any lockout.
func (a *Auth) Rotate() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.otp = newOTP()
	a.fails = 0
	a.lockedTo = time.Time{}
	return a.otp
}

// Login checks a password and, on success, returns a session token. A password
// can only be used once; afterwards the session cookie is the credential.
func (a *Auth) Login(pw string) (string, error) {
	norm := normalizeOTP(pw)

	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	if now.Before(a.lockedTo) {
		return "", fmt.Errorf("%w - try again in %s", errLocked, time.Until(a.lockedTo).Round(time.Second))
	}
	if a.otp == "" {
		return "", errBurned
	}
	if len(norm) != otpLength || subtle.ConstantTimeCompare([]byte(norm), []byte(normalizeOTP(a.otp))) != 1 {
		a.fails++
		if d := lockoutFor(a.fails); d > 0 {
			a.lockedTo = now.Add(d)
		}
		return "", errBadPassword
	}

	a.otp = "" // single use
	a.fails = 0
	tok := randomToken()
	a.tokens[tok] = now.Add(tokenTTL)
	return tok, nil
}

// lockoutFor throttles guessing: the first two misses are free, after that
// every miss costs an exponentially growing pause.
func lockoutFor(fails int) time.Duration {
	if fails < 3 {
		return 0
	}
	d := 10 * time.Second << min(fails-3, 6)
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

func (a *Auth) Valid(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.tokens[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.tokens, token)
		return false
	}
	return true
}

func (a *Auth) Logout(token string) {
	a.mu.Lock()
	delete(a.tokens, token)
	a.mu.Unlock()
}

func (a *Auth) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return a.Valid(c.Value)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(tokenTTL.Seconds()),
	})
}

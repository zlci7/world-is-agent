package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

const sessionCookieName = "wia_session"

// sessionStore owns the two credentials the control plane has: the bootstrap
// token the Runtime prints, and the session credentials the browser stores.
//
// The bootstrap token is never typed by the user. The Runtime opens the browser
// with the token in the URL fragment, the client posts it once to exchange for a
// session, and the fragment is dropped. Keeping it valid for the process
// lifetime (rather than single use) means a second browser, or a reload after
// clearing cookies, does not force a Runtime restart.
type sessionStore struct {
	mu        sync.Mutex
	bootstrap string
	sessions  map[string]struct{}
}

func newSessionStore() (*sessionStore, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	return &sessionStore{bootstrap: token, sessions: make(map[string]struct{})}, nil
}

// bootstrapToken is the value the Runtime prints and opens the browser with.
func (s *sessionStore) bootstrapToken() string { return s.bootstrap }

// issue exchanges a bootstrap token for a session credential.
func (s *sessionStore) issue(candidate string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || subtle.ConstantTimeCompare([]byte(candidate), []byte(s.bootstrap)) != 1 {
		return "", false
	}
	value, err := randomToken()
	if err != nil {
		return "", false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[value] = struct{}{}
	return value, true
}

// valid reports whether the request carries a session credential.
func (s *sessionStore) valid(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[cookie.Value]
	return ok
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("httpapi: generate a credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

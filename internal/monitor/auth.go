package monitor

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "bus_session"
	sessionIdle   = 12 * time.Hour     // a session unused for this long ends
	sessionMax    = 7 * 24 * time.Hour // a session ends this long after login, however busy it is

	freeLoginFailures = 5                // wrong passwords one client may send before it is slowed down
	maxLoginDelay     = 15 * time.Minute // the delay after the free failures doubles from 1 s up to this
	forgetFailures    = 15 * time.Minute // a client that waited this long since its last failure starts afresh
)

var (
	errUnauthenticated = errors.New("log in first: POST /api/v1/login, or send Authorization: Bearer <token> or Basic")
	errBadCredentials  = errors.New("wrong password")
)

// tooManyAttempts is the refusal of a client that sent too many wrong passwords.
type tooManyAttempts struct{ retryAfter time.Duration }

func (tooManyAttempts) Error() string { return "too many failed logins, wait before trying again" }

type session struct{ created, lastUsed time.Time }

type loginFailures struct {
	count int
	last  time.Time
}

// auth checks the password and keeps the login sessions in memory. They end with the process, so a
// restart (which a new password needs anyway) logs everybody out.
type auth struct {
	hash []byte // bcrypt; empty means the monitor has no login
	now  func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	failures map[string]*loginFailures // by client address
}

func newAuth(hash []byte, now func() time.Time) *auth {
	return &auth{hash: hash, now: now, sessions: map[string]*session{}, failures: map[string]*loginFailures{}}
}

func (a *auth) enabled() bool { return len(a.hash) > 0 }

// attempt checks a password for a client. Every attempt counts against the client before the
// password is compared, so guesses sent at the same time cannot slip past the limit; a correct
// password clears the count. The delay grows with every further failure.
func (a *auth) attempt(address, password string) error {
	a.mu.Lock()
	now := a.now()
	for key, failures := range a.failures {
		if now.Sub(failures.last) > forgetFailures {
			delete(a.failures, key)
		}
	}
	if failures := a.failures[address]; failures != nil && failures.count >= freeLoginFailures {
		delay := min(time.Second<<min(failures.count-freeLoginFailures, 20), maxLoginDelay)
		if wait := failures.last.Add(delay).Sub(now); wait > 0 {
			a.mu.Unlock()
			return tooManyAttempts{wait}
		}
	}
	if a.failures[address] == nil {
		a.failures[address] = &loginFailures{}
	}
	a.failures[address].count++
	a.failures[address].last = now
	a.mu.Unlock()

	if bcrypt.CompareHashAndPassword(a.hash, []byte(password)) != nil {
		return errBadCredentials
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failures, address)
	return nil
}

// startSession makes a new session, always: a login never keeps the token it came with.
func (a *auth) startSession(replacing string) string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(err) // the system's random source is broken
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, replacing)
	now := a.now()
	for key, existing := range a.sessions {
		if a.expired(existing, now) {
			delete(a.sessions, key)
		}
	}
	a.sessions[token] = &session{created: now, lastUsed: now}
	return token
}

func (a *auth) expired(s *session, now time.Time) bool {
	return now.Sub(s.lastUsed) > sessionIdle || now.Sub(s.created) > sessionMax
}

// useSession says whether the token is a live session, and counts the use as activity.
func (a *auth) useSession(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[token]
	if s == nil || a.expired(s, a.now()) {
		delete(a.sessions, token)
		return false
	}
	s.lastUsed = a.now()
	return true
}

// isLive says whether the session still exists, without counting as activity: an open event
// stream does not keep a session alive.
func (a *auth) isLive(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[token]
	return s != nil && !a.expired(s, a.now())
}

func (a *auth) endSession(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

// principal says how a request proved who it is.
type principal struct {
	token    string // the session, empty for Basic or an open monitor
	byHeader bool   // with an Authorization header, which a web page of another origin cannot send
}

// authenticate reads Authorization (Bearer session token, or Basic with the password) or the
// session cookie.
func (a *auth) authenticate(r *http.Request, address string) (principal, error) {
	if !a.enabled() {
		return principal{}, nil
	}
	if header := r.Header.Get("Authorization"); header != "" {
		// The scheme name is case-insensitive (RFC 9110).
		if scheme, token, found := strings.Cut(header, " "); found && strings.EqualFold(scheme, "Bearer") {
			token = strings.TrimSpace(token)
			if a.useSession(token) {
				return principal{token: token, byHeader: true}, nil
			}
			return principal{}, errUnauthenticated
		}
		if _, password, isBasic := r.BasicAuth(); isBasic {
			if err := a.attempt(address, password); err != nil {
				return principal{}, err
			}
			return principal{byHeader: true}, nil
		}
		return principal{}, errUnauthenticated
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil && a.useSession(cookie.Value) {
		return principal{token: cookie.Value}, nil
	}
	return principal{}, errUnauthenticated
}

type principalKey struct{}

func principalOf(r *http.Request) principal {
	who, _ := r.Context().Value(principalKey{}).(principal)
	return who
}

// gate puts the login in front of the API.
type gate struct {
	access Access
	auth   *auth
}

// guard lets a request through when it is logged in (or the monitor has no login). A mutating
// request must also come from the page's own origin, or carry an Authorization header: that is
// what stops another site's page from sending it with the browser's cookie.
func (g gate) guard(mutating bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view := g.access.clientOf(r)
		who, err := g.auth.authenticate(r, view.address)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		if mutating && !g.fromOwnOrigin(r, view, who) {
			writeError(w, http.StatusForbidden, "forbidden_origin", "the request does not come from this page's origin")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, who)))
	}
}

// fromOwnOrigin judges Origin, or Referer when there is none. A request with neither is no browser's
// (browsers always send Origin on a POST), which only matters when a cookie could be behind it.
func (g gate) fromOwnOrigin(r *http.Request, view client, who principal) bool {
	if who.byHeader {
		return true
	}
	for _, name := range []string{"Origin", "Referer"} {
		if value := r.Header.Get(name); value != "" {
			return view.sameOrigin(value)
		}
	}
	return !g.auth.enabled()
}

func writeAuthError(w http.ResponseWriter, err error) {
	var tooMany tooManyAttempts
	switch {
	case errors.As(err, &tooMany):
		w.Header().Set("Retry-After", strconv.Itoa(int(tooMany.retryAfter/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", err.Error())
	case errors.Is(err, errBadCredentials):
		writeError(w, http.StatusUnauthorized, "bad_credentials", err.Error())
	default:
		writeError(w, http.StatusUnauthorized, "unauthenticated", err.Error())
	}
}

// serveLogin checks the password and answers {"token": ...} besides setting the session cookie.
// A page of another origin is refused (Origin), but a client without Origin may log in.
func (g gate) serveLogin(w http.ResponseWriter, r *http.Request) {
	view := g.access.clientOf(r)
	if !g.auth.enabled() {
		writeError(w, http.StatusConflict, "no_password_set", "the monitor has no password: set one with `busctl monitor password`")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !view.sameOrigin(origin) {
		writeError(w, http.StatusForbidden, "forbidden_origin", "the request does not come from this page's origin")
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "login wants Content-Type: application/json")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := g.auth.attempt(view.address, request.Password); err != nil {
		writeAuthError(w, err)
		return
	}
	var previous string
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		previous = cookie.Value
	}
	token := g.auth.startSession(previous)
	setSessionCookie(w, token, int(sessionMax/time.Second), view.secure)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{"token": token})
}

// serveLogout ends the session the request used and clears the cookie.
func (g gate) serveLogout(w http.ResponseWriter, r *http.Request) {
	g.auth.endSession(principalOf(r).token)
	setSessionCookie(w, "", -1, g.access.clientOf(r).secure)
	w.WriteHeader(http.StatusNoContent)
}

func setSessionCookie(w http.ResponseWriter, token string, maxAgeSeconds int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAgeSeconds,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure,
	})
}

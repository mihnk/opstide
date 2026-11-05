// Package auth protects the API with one shared token.
//
// OpsTide has no user accounts: whoever holds the token can read the record.
// A script sends it as "Authorization: Bearer <token>". The browser signs in
// once with it and gets a cookie that carries a value derived from the token
// (never the token itself), so a token rotation signs everyone out. The
// probes, the page itself and its scripts stay open: they hold no data.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	cookieName  = "opstide_session"
	sessionAge  = 30 * 24 * time.Hour
	failBurst   = 5
	maxLimiters = 10_000
	// MinTokenLength is the shortest token accepted: a short one can be guessed.
	MinTokenLength = 16
)

// Auth checks requests against a token. The zero value, and one made with
// an empty token, lets everything through.
type Auth struct {
	token   [sha256.Size]byte
	key     []byte
	enabled bool

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	now      func() time.Time
}

// New returns an Auth for token; with an empty token it is disabled.
func New(token string) *Auth {
	a := &Auth{limiters: map[string]*rate.Limiter{}, now: time.Now}
	if token == "" {
		return a
	}
	a.enabled = true
	a.token = sha256.Sum256([]byte(token))
	a.key = []byte(token)
	return a
}

// LoadToken reads the token from file (a mounted Secret), or from env when
// there is no file. Whitespace around it is dropped. A file that is empty is
// an error: it would silently turn the login off.
func LoadToken(file, env string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("token file: %w", err)
		}
		t := strings.TrimSpace(string(b))
		if t == "" {
			return "", errors.New("token file " + file + " is empty")
		}
		return t, checkLength(t)
	}
	t := strings.TrimSpace(env)
	if t == "" {
		return "", nil
	}
	return t, checkLength(t)
}

func checkLength(t string) error {
	if len(t) < MinTokenLength {
		return fmt.Errorf("the token is %d bytes; use at least %d (for example openssl rand -hex 20)", len(t), MinTokenLength)
	}
	return nil
}

// Enabled reports whether a token is required.
func (a *Auth) Enabled() bool { return a != nil && a.enabled }

// open are the API paths that answer without a token: they say whether one
// is needed and take it.
var open = map[string]bool{"/api/v1/session": true, "/api/v1/login": true, "/api/v1/logout": true}

func protected(path string) bool {
	return (strings.HasPrefix(path, "/api/") && !open[path]) || path == "/metrics"
}

// Allowed reports whether r carries the token, as a Bearer header or as a
// valid session cookie. A Bearer header that is not the token does not hide
// a good cookie: a proxy in front may add an Authorization header of its own.
func (a *Auth) Allowed(r *http.Request) bool {
	ok, _ := a.check(r)
	return ok
}

// check says whether r is allowed, and whether it presented a wrong Bearer
// token (which the caller charges to the address's failure limit).
func (a *Auth) check(r *http.Request) (ok, wrongBearer bool) {
	if !a.Enabled() {
		return true, false
	}
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		sum := sha256.Sum256([]byte(strings.TrimSpace(h[7:])))
		if subtle.ConstantTimeCompare(sum[:], a.token[:]) == 1 {
			return true, false
		}
		wrongBearer = true
	}
	if c, err := r.Cookie(cookieName); err == nil && a.validSession(c.Value) {
		return true, false
	}
	return false, wrongBearer
}

// authorize is check plus the failure limit: it answers false and writes
// the 429 when the address has used up its wrong guesses.
func (a *Auth) authorize(w http.ResponseWriter, r *http.Request) (ok, limited bool) {
	ip := clientKey(r)
	if a.Enabled() && !a.hasTry(ip) {
		tooMany(w)
		return false, true
	}
	ok, wrong := a.check(r)
	if wrong && !ok {
		a.fail(ip)
	}
	return ok, false
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	reply(w, http.StatusTooManyRequests, map[string]any{"error": "too many wrong tokens; wait a minute"})
}

// Middleware answers 401 for protected paths without the token.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if protected(r.URL.Path) {
			ok, limited := a.authorize(w, r)
			if limited {
				return
			}
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="opstide"`)
				reply(w, http.StatusUnauthorized, map[string]any{"error": "sign in with the token"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Register adds /api/v1/session, /login and /logout.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		ok, limited := a.authorize(w, r)
		if limited {
			return
		}
		reply(w, http.StatusOK, map[string]any{"authRequired": a.Enabled(), "authenticated": ok})
	})
	mux.HandleFunc("POST /api/v1/login", a.login)
	mux.HandleFunc("POST /api/v1/logout", func(w http.ResponseWriter, r *http.Request) {
		// A form on another site cannot send JSON without a preflight.
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			reply(w, http.StatusUnsupportedMediaType, map[string]any{"error": "send application/json"})
			return
		}
		http.SetCookie(w, a.cookie(r, "", -1))
		reply(w, http.StatusOK, map[string]any{"ok": true})
	})
}

func (a *Auth) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: cookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		// Behind a TLS-terminating proxy the request itself is plain HTTP.
		Secure: r.TLS != nil || forwardedHTTPS(r),
	}
}

// forwardedHTTPS reads the first value of X-Forwarded-Proto ("https,http").
func forwardedHTTPS(r *http.Request) bool {
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(first), "https")
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if !a.Enabled() {
		reply(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ip := clientKey(r)
	if !a.hasTry(ip) {
		tooMany(w)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		reply(w, http.StatusBadRequest, map[string]any{"error": "send {\"token\": \"…\"}"})
		return
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(body.Token)))
	if subtle.ConstantTimeCompare(sum[:], a.token[:]) != 1 {
		a.fail(ip)
		reply(w, http.StatusUnauthorized, map[string]any{"error": "that is not the token"})
		return
	}
	http.SetCookie(w, a.cookie(r, a.newSession(), int(sessionAge/time.Second)))
	reply(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *Auth) limiter(key string) *rate.Limiter {
	l := a.limiters[key]
	if l != nil {
		return l
	}
	if len(a.limiters) >= maxLimiters {
		// Forget addresses that are not being held back; if every one is, a new
		// address is held back too rather than letting a flood clear its own limit.
		for k, v := range a.limiters {
			if v.TokensAt(a.now()) >= failBurst {
				delete(a.limiters, k)
			}
		}
		if len(a.limiters) >= maxLimiters {
			l = rate.NewLimiter(0, 0)
			return l
		}
	}
	l = rate.NewLimiter(rate.Every(10*time.Second), failBurst)
	a.limiters[key] = l
	return l
}

// hasTry reports whether the address may still try a token. Only wrong
// tokens use it up, so signing in correctly (many people behind one proxy
// address after a rotation) never exhausts it.
func (a *Auth) hasTry(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.limiter(key).TokensAt(a.now()) >= 1
}

// fail charges one wrong token to the address: five at once, then one
// more every 10 seconds.
func (a *Auth) fail(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.limiter(key).AllowN(a.now(), 1)
}

// clientKey is the connection's own address (an IPv6 one by its /64). A
// forwarded header can be forged, so it is not used; behind a proxy all
// users share the proxy's bucket, which only wrong tokens spend.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() && !ip.Is4In6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return host
}

// newSession returns a cookie value: when it was issued and a MAC of that
// time under the token. The server refuses it after sessionAge; changing
// the token refuses all of them at once. Signing out only removes the
// browser's copy.
func (a *Auth) newSession() string {
	ts := strconv.FormatInt(a.now().Unix(), 10)
	return ts + "." + a.mac(ts)
}

func (a *Auth) mac(ts string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte("opstide-session-v2." + ts))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Auth) validSession(v string) bool {
	ts, mac, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(mac), []byte(a.mac(ts))) != 1 {
		return false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := a.now().Sub(time.Unix(n, 0))
	return age >= -time.Minute && age < sessionAge
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

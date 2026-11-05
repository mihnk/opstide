package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func server(t *testing.T, token string) (*httptest.Server, *Auth) {
	t.Helper()
	a := New(token)
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("/api/v1/day", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"secret":"data"}`)) })
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("up 1\n")) })
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) })
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, a
}

func do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, b.String()
}

func get(t *testing.T, srv *httptest.Server, path string, hdr map[string]string, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return do(t, req)
}

func login(t *testing.T, srv *httptest.Server, token string) (*http.Response, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/login", strings.NewReader(string(body)))
	return do(t, req)
}

func TestWithoutATokenNothingIsProtected(t *testing.T) {
	srv, a := server(t, "")
	if a.Enabled() {
		t.Fatal("enabled without a token")
	}
	if resp, body := get(t, srv, "/api/v1/day", nil); resp.StatusCode != 200 || !strings.Contains(body, "secret") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	_, body := get(t, srv, "/api/v1/session", nil)
	if !strings.Contains(body, `"authRequired":false`) || !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("session %s", body)
	}
}

func TestTheAPIAndMetricsNeedTheTokenButTheRestDoesNot(t *testing.T) {
	srv, _ := server(t, "s3cret-token")
	for _, p := range []string{"/api/v1/day", "/api/v1/anything", "/metrics"} {
		resp, body := get(t, srv, p, nil)
		if resp.StatusCode != 401 || strings.Contains(body, "secret") || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: %d %q", p, resp.StatusCode, body)
		}
	}
	for _, p := range []string{"/healthz", "/", "/assets/index.js", "/api/v1/session"} {
		if resp, _ := get(t, srv, p, nil); resp.StatusCode != 200 {
			t.Errorf("%s should be open: %d", p, resp.StatusCode)
		}
	}
	// Bearer, exactly.
	if resp, _ := get(t, srv, "/api/v1/day", map[string]string{"Authorization": "Bearer s3cret-token"}); resp.StatusCode != 200 {
		t.Errorf("bearer: %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv, "/metrics", map[string]string{"Authorization": "Bearer s3cret-token"}); resp.StatusCode != 200 {
		t.Errorf("metrics with bearer: %d", resp.StatusCode)
	}
	for _, h := range []string{"Bearer s3cret-toke", "Bearer s3cret-token-", "Basic czNjcmV0LXRva2Vu", "s3cret-token", "Bearer "} {
		if resp, _ := get(t, srv, "/api/v1/day", map[string]string{"Authorization": h}); resp.StatusCode != 401 {
			t.Errorf("%q accepted: %d", h, resp.StatusCode)
		}
	}
}

func TestSigningInGivesACookieThatIsNotTheToken(t *testing.T) {
	srv, _ := server(t, "s3cret-token")
	if resp, _ := login(t, srv, "wrong"); resp.StatusCode != 401 || len(resp.Cookies()) != 0 {
		t.Fatalf("wrong token: %d %v", resp.StatusCode, resp.Cookies())
	}
	resp, _ := login(t, srv, "  s3cret-token\n")
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
		t.Fatalf("login: %d %v", resp.StatusCode, resp.Cookies())
	}
	c := resp.Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure || strings.Contains(c.Value, "s3cret") || c.MaxAge != 30*24*3600 {
		t.Fatalf("cookie %+v", c)
	}
	if resp, body := get(t, srv, "/api/v1/day", nil, c); resp.StatusCode != 200 || !strings.Contains(body, "secret") {
		t.Fatalf("with cookie: %d", resp.StatusCode)
	}
	_, body := get(t, srv, "/api/v1/session", nil, c)
	if !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("session %s", body)
	}
	if resp, _ := get(t, srv, "/api/v1/day", nil, &http.Cookie{Name: cookieName, Value: c.Value + "x"}); resp.StatusCode != 401 {
		t.Fatalf("a forged cookie: %d", resp.StatusCode)
	}
	// A different token means a different cookie: rotating the token signs everyone out.
	other, _ := server(t, "another-token")
	if resp, _ := get(t, other, "/api/v1/day", nil, c); resp.StatusCode != 401 {
		t.Fatalf("old cookie after rotation: %d", resp.StatusCode)
	}
	// Behind a TLS proxy the cookie is Secure.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/login", strings.NewReader(`{"token":"s3cret-token"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	if resp, _ := do(t, req); !resp.Cookies()[0].Secure {
		t.Fatal("cookie not Secure behind https")
	}
	// Signing out clears it.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logout", nil)
	if resp, _ := do(t, req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("logout without JSON (a cross-site form): %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logout", nil)
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := do(t, req); resp.Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout cookie %+v", resp.Cookies()[0])
	}
}

func TestGuessingIsSlowedDown(t *testing.T) {
	srv, _ := server(t, "s3cret-token")
	var codes []int
	for range 8 {
		resp, _ := login(t, srv, "guess")
		codes = append(codes, resp.StatusCode)
	}
	if codes[4] != 401 || codes[5] != 429 || codes[7] != 429 {
		t.Fatalf("five tries, then slowed: %v", codes)
	}
	// Even the right token waits its turn from the same address.
	if resp, _ := login(t, srv, "s3cret-token"); resp.StatusCode != 429 {
		t.Fatalf("right token while limited: %d", resp.StatusCode)
	}
	// Wrong Bearer tokens spend the same tries, on the API and on /session.
	srv3, _ := server(t, "s3cret-token")
	var wrong []int
	for range 8 {
		resp, _ := get(t, srv3, "/api/v1/session", map[string]string{"Authorization": "Bearer guess"})
		wrong = append(wrong, resp.StatusCode)
	}
	if wrong[4] != 200 || wrong[5] != 429 {
		t.Fatalf("bearer guesses: %v", wrong)
	}
	if resp, _ := get(t, srv3, "/api/v1/day", map[string]string{"Authorization": "Bearer s3cret-token"}); resp.StatusCode != 429 {
		t.Fatalf("even the right bearer waits while the address is held back: %d", resp.StatusCode)
	}
	// The API itself is never slowed for someone who has the token.
	srv4, _ := server(t, "s3cret-token")
	for range 20 {
		if resp, _ := get(t, srv4, "/api/v1/day", map[string]string{"Authorization": "Bearer s3cret-token"}); resp.StatusCode != 200 {
			t.Fatal("limited a valid bearer")
		}
	}
	// Bad bodies are refused, not crashed on.
	srv2, _ := server(t, "s3cret-token")
	req, _ := http.NewRequest(http.MethodPost, srv2.URL+"/api/v1/login", strings.NewReader("not json"))
	if resp, _ := do(t, req); resp.StatusCode != 400 {
		t.Fatalf("bad body: %d", resp.StatusCode)
	}
}

func TestLoadToken(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "token")
	if err := os.WriteFile(f, []byte("  from-file-token-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadToken(f, "from-env-token-1234"); err != nil || got != "from-file-token-123" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := LoadToken("", " from-env-token-1234 "); err != nil || got != "from-env-token-1234" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := LoadToken("", ""); err != nil || got != "" {
		t.Fatalf("no token: %q %v", got, err)
	}
	empty := filepath.Join(dir, "empty")
	_ = os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := LoadToken(empty, ""); err == nil {
		t.Fatal("an empty token file must not turn the login off")
	}
	if _, err := LoadToken(filepath.Join(dir, "missing"), ""); err == nil {
		t.Fatal("a missing token file is an error")
	}
}

func TestRightTokensNeverSpendTheTries(t *testing.T) {
	// Behind one proxy address every user looks the same; signing in right
	// (after a rotation, 50 people do) must not lock the rest out.
	srv, _ := server(t, "s3cret-token")
	for i := range 50 {
		resp, _ := login(t, srv, "s3cret-token")
		if resp.StatusCode != 200 {
			t.Fatalf("login %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := get(t, srv, "/api/v1/day", map[string]string{"Authorization": "Bearer s3cret-token"}); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
}

func TestAProxysAuthorizationHeaderDoesNotHideAGoodCookie(t *testing.T) {
	srv, _ := server(t, "s3cret-token")
	resp, _ := login(t, srv, "s3cret-token")
	c := resp.Cookies()[0]
	// oauth2-proxy and friends pass their own bearer token upstream.
	if resp, _ := get(t, srv, "/api/v1/day", map[string]string{"Authorization": "Bearer an-id-token-from-the-proxy"}, c); resp.StatusCode != 200 {
		t.Fatalf("cookie with a foreign bearer: %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv, "/api/v1/day", map[string]string{"Authorization": "BEARER s3cret-token"}); resp.StatusCode != 200 {
		t.Fatalf("scheme is case-insensitive: %d", resp.StatusCode)
	}
}

func TestSessionsExpireOnTheServer(t *testing.T) {
	a := New("s3cret-token")
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }
	v := a.newSession()
	if !a.validSession(v) {
		t.Fatal("fresh session refused")
	}
	now = now.Add(sessionAge - time.Hour)
	if !a.validSession(v) {
		t.Fatal("session refused before it expires")
	}
	now = now.Add(2 * time.Hour)
	if a.validSession(v) {
		t.Fatal("expired session accepted")
	}
	// An older timestamp with a copied MAC is a different session.
	ts, mac, _ := strings.Cut(v, ".")
	if a.validSession("1."+mac) || a.validSession(ts) || a.validSession("") || a.validSession(".") {
		t.Fatal("malformed session accepted")
	}
}

func TestForwardedProto(t *testing.T) {
	for v, want := range map[string]bool{"https": true, "HTTPS": true, "https,http": true, " https , http": true, "http": false, "http,https": false, "": false} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Forwarded-Proto", v)
		if got := forwardedHTTPS(r); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
}

func TestAFloodOfAddressesDoesNotClearAnyonesLimit(t *testing.T) {
	a := New("s3cret-token")
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }
	for range failBurst {
		a.fail("attacker")
	}
	if a.hasTry("attacker") {
		t.Fatal("attacker still has tries")
	}
	for i := range maxLimiters + 500 {
		a.fail("flood-" + strconv.Itoa(i)) // each uses one try: none is full again
	}
	if a.hasTry("attacker") {
		t.Fatal("the flood cleared the attacker's limit")
	}
	if len(a.limiters) > maxLimiters+1 {
		t.Fatalf("limiters grew to %d", len(a.limiters))
	}
}

func TestIPv6AddressesShareTheirSlash64(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[2001:db8:1:2:aaaa::1]:5000"
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "[2001:db8:1:2:bbbb::9]:6000"
	if clientKey(r) != clientKey(r2) {
		t.Fatal("two hosts of one /64 get separate limits")
	}
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.RemoteAddr = "10.1.2.3:80"
	if clientKey(r3) != "10.1.2.3" {
		t.Fatal(clientKey(r3))
	}
}

func TestShortTokensAreRefused(t *testing.T) {
	if _, err := LoadToken("", "short"); err == nil {
		t.Fatal("a 5-byte token was accepted")
	}
	f := filepath.Join(t.TempDir(), "t")
	_ = os.WriteFile(f, []byte("short\n"), 0o600)
	if _, err := LoadToken(f, ""); err == nil {
		t.Fatal("a short token file was accepted")
	}
}

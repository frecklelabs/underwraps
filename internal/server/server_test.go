package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/frecklelabs/underwraps/internal/data"
	jose "github.com/go-jose/go-jose/v4"
)

type authorization struct{ Subject, Nonce, Challenge string }
type testIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]authorization
	mode   string
}

func newIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &testIssuer{key: key, codes: map[string]authorization{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			send(w, 200, map[string]any{"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			send(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("choose") == "" {
				w.Header().Set("Content-Type", "text/html")
				q.Set("choose", "owner")
				owner := "/authorize?" + q.Encode()
				q.Set("choose", "second")
				second := "/authorize?" + q.Encode()
				_, _ = fmt.Fprintf(w, `<a href="%s">Owner</a><a href="%s">Second</a>`, owner, second)
				return
			}
			code := randomToken()
			f.mu.Lock()
			f.codes[code] = authorization{q.Get("choose"), q.Get("nonce"), q.Get("code_challenge")}
			f.mu.Unlock()
			callback, _ := url.Parse(q.Get("redirect_uri"))
			cq := callback.Query()
			cq.Set("code", code)
			cq.Set("state", q.Get("state"))
			callback.RawQuery = cq.Encode()
			http.Redirect(w, r, callback.String(), 302)
		case "/token":
			_ = r.ParseForm()
			f.mu.Lock()
			a, ok := f.codes[r.Form.Get("code")]
			delete(f.codes, r.Form.Get("code"))
			mode := f.mode
			f.mu.Unlock()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(hash[:]) != a.Challenge {
				http.Error(w, "invalid grant", 400)
				return
			}
			claims := f.claims(a.Subject, "browser")
			claims["nonce"] = a.Nonce
			switch mode {
			case "nonce":
				claims["nonce"] = "wrong"
			case "issuer":
				claims["iss"] = "https://other.example.invalid"
			case "audience":
				claims["aud"] = "wrong"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "subject":
				claims["sub"] = ""
			case "groups":
				claims["groups"] = "wrong-type"
			}
			raw := f.sign(t, claims)
			if mode == "signature" {
				parts := strings.Split(raw, ".")
				parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
				raw = strings.Join(parts, ".")
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *testIssuer) claims(subject, audience string) map[string]any {
	return map[string]any{"iss": f.server.URL, "sub": subject, "aud": audience, "exp": time.Now().Add(50 * time.Minute).Unix(), "iat": time.Now().Unix(), "name": strings.ToUpper(subject[:1]) + subject[1:], "groups": []string{"fixture-family"}}
}
func (f *testIssuer) sign(t *testing.T, c map[string]any) string {
	t.Helper()
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT"))
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(c)
	signed, e := signer.Sign(raw)
	if e != nil {
		t.Fatal(e)
	}
	out, e := signed.CompactSerialize()
	if e != nil {
		t.Fatal(e)
	}
	return out
}

type fixture struct {
	s             *Server
	issuer        *testIssuer
	store         *data.Store
	handler       http.Handler
	owner, second principal
	advance       *atomic.Bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := newIssuer(t)
	advance := &atomic.Bool{}
	store, e := data.Open(data.Config{Path: t.TempDir() + "/app.sqlite", Timezone: "UTC", Clock: func() time.Time {
		if advance.Load() {
			return time.Now().AddDate(0, 0, 10)
		}
		return time.Now()
	}, Issuer: f.server.URL, Groups: []data.GroupMapping{{Claim: "fixture-family", ID: 1, Name: "Fixture family"}, {Claim: "fixture-extended", ID: 2, Name: "Fixture extended"}}, AdministratorGroups: []string{"fixture-admin"}, Administrators: []data.Identity{{Issuer: f.server.URL, Subject: "owner"}}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = store.Close() })
	s, e := New(oidc.ClientContext(context.Background(), f.server.Client()), Config{Origin: "https://app.example.invalid", Issuer: f.server.URL, ClientID: "browser", MCPAudience: "mcp", GroupsClaim: "groups", Scopes: []string{"groups"}}, store)
	if e != nil {
		t.Fatal(e)
	}
	owner := principal{Identity: data.Identity{Issuer: f.server.URL, Subject: "owner"}, Name: "Owner", Groups: []string{"fixture-family"}}
	second := principal{Identity: data.Identity{Issuer: f.server.URL, Subject: "second"}, Name: "Second", Groups: []string{"fixture-family"}}
	return &fixture{advance: advance, s: s, issuer: f, store: store, handler: s.Handler(), owner: owner, second: second}
}
func (f *fixture) login(t *testing.T, subject string, old ...*http.Cookie) (*http.Cookie, string) {
	t.Helper()
	start := httptest.NewRecorder()
	f.handler.ServeHTTP(start, httptest.NewRequest("GET", "https://app.example.invalid/auth/login", nil))
	if start.Code != 302 {
		t.Fatalf("login %d", start.Code)
	}
	q := mustURL(t, start.Header().Get("Location")).Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatal("missing OIDC protections")
	}
	code := randomToken()
	f.issuer.mu.Lock()
	f.issuer.codes[code] = authorization{subject, q.Get("nonce"), q.Get("code_challenge")}
	f.issuer.mu.Unlock()
	callback := httptest.NewRequest("GET", "https://app.example.invalid/auth/callback?code="+code+"&state="+q.Get("state"), nil)
	callback.AddCookie(start.Result().Cookies()[0])
	for _, c := range old {
		callback.AddCookie(c)
	}
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, callback)
	if out.Code != 303 {
		t.Fatalf("callback %d %s", out.Code, out.Body)
	}
	for _, c := range out.Result().Cookies() {
		if c.Name == "__Host-uw-session" {
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe cookie")
			}
			me := f.http(t, c, "", "me", map[string]any{}, 200)
			var v struct{ CSRF string }
			_ = json.Unmarshal(me, &v)
			return c, v.CSRF
		}
	}
	t.Fatal("no session")
	return nil, ""
}
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func (f *fixture) http(t *testing.T, c *http.Cookie, csrf, op string, args any, status int) []byte {
	t.Helper()
	b, _ := json.Marshal(args)
	r := httptest.NewRequest("POST", "https://app.example.invalid/api/"+op, bytes.NewReader(b))
	if c != nil {
		r.AddCookie(c)
	}
	r.Header.Set("Origin", f.s.cfg.Origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s got %d want %d: %s", op, w.Code, status, w.Body)
	}
	return w.Body.Bytes()
}
func (f *fixture) mcp(t *testing.T, token, op string, args any, status int) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": op, "arguments": args}})
	r := httptest.NewRequest("POST", "https://app.example.invalid/mcp", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("X-Viewer-ID", "owner")
	r.Header.Set("X-Forwarded-User", "owner")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("mcp %s got %d want %d: %s", op, w.Code, status, w.Body)
	}
	return w.Body.Bytes()
}
func TestOIDCLoginAndSessions(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.login(t, "owner")
	f.http(t, cookie, csrf, "lists", map[string]any{}, 200)
	f.http(t, nil, csrf, "lists", map[string]any{}, 401)
	forged := *cookie
	forged.Value = randomToken()
	f.http(t, &forged, csrf, "lists", map[string]any{}, 401)
	f.http(t, cookie, "forged", "create_list", map[string]any{}, 403)
	r := httptest.NewRequest("POST", "https://app.example.invalid/api/lists", strings.NewReader("{}"))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://attacker.example.invalid")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	// Account binding uses subject, not display name; disabled users cannot sign back in.
	user, e := f.store.Register(context.Background(), f.owner.Identity, "Owner")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.SetUser(context.Background(), f.owner.Identity, user.ID, "Owner", true); e != nil {
		t.Fatal(e)
	}
	f.http(t, cookie, csrf, "lists", map[string]any{}, 403)
}
func TestOIDCInvalidTokensAndState(t *testing.T) {
	f := newFixture(t)
	for _, mode := range []string{"nonce", "issuer", "audience", "expired", "subject", "groups", "signature", "state", "pkce", "expired-flow"} {
		t.Run(mode, func(t *testing.T) {
			f.issuer.mu.Lock()
			f.issuer.mode = mode
			f.issuer.mu.Unlock()
			start := httptest.NewRecorder()
			f.handler.ServeHTTP(start, httptest.NewRequest("GET", "/auth/login", nil))
			q := mustURL(t, start.Header().Get("Location")).Query()
			code := randomToken()
			challenge := q.Get("code_challenge")
			if mode == "pkce" {
				challenge = "wrong"
			}
			f.issuer.mu.Lock()
			f.issuer.codes[code] = authorization{"owner", q.Get("nonce"), challenge}
			f.issuer.mu.Unlock()
			state := q.Get("state")
			if mode == "state" {
				state = "wrong"
			}
			if mode == "expired-flow" {
				f.s.mu.Lock()
				for k, v := range f.s.logins {
					v.Expiry = time.Now().Add(-time.Hour)
					f.s.logins[k] = v
				}
				f.s.mu.Unlock()
			}
			r := httptest.NewRequest("GET", "/auth/callback?code="+code+"&state="+state, nil)
			r.AddCookie(start.Result().Cookies()[0])
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("accepted %s: %d", mode, w.Code)
			}
			w = httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatal("flow replay accepted")
			}
		})
	}
}
func TestSessionRotationLogoutExpiry(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.login(t, "owner")
	old := cookie
	cookie, csrf = f.login(t, "owner", old)
	f.http(t, old, csrf, "lists", map[string]any{}, 401)
	f.http(t, cookie, csrf, "logout", map[string]any{}, 200)
	f.http(t, cookie, csrf, "lists", map[string]any{}, 401)
	cookie, csrf = f.login(t, "owner")
	f.s.mu.Lock()
	v := f.s.sessions[cookie.Value]
	v.Expiry = time.Now().Add(-time.Minute)
	f.s.sessions[cookie.Value] = v
	f.s.mu.Unlock()
	f.http(t, cookie, csrf, "lists", map[string]any{}, 401)
}
func TestForgedMCPCallerRejected(t *testing.T) {
	f := newFixture(t)
	f.mcp(t, "", "lists", map[string]any{}, 401)
	for _, mode := range []string{"wrong-audience", "wrong-issuer", "expired", "signature", "subject", "groups"} {
		claims := f.issuer.claims("owner", "mcp")
		switch mode {
		case "wrong-audience":
			claims["aud"] = "browser"
		case "wrong-issuer":
			claims["iss"] = "https://wrong.example.invalid"
		case "expired":
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
		case "subject":
			claims["sub"] = ""
		case "groups":
			claims["groups"] = 4
		}
		token := f.issuer.sign(t, claims)
		if mode == "signature" {
			token += "x"
		}
		f.mcp(t, token, "lists", map[string]any{}, 401)
	}
	token := f.issuer.sign(t, f.issuer.claims("owner", "mcp"))
	out := f.mcp(t, token, "lists", map[string]any{"viewer": "second"}, 200)
	if !bytes.Contains(out, []byte(`"isError":true`)) {
		t.Fatalf("viewer accepted: %s", out)
	}
	out = f.mcp(t, token, "lists", map[string]any{}, 200)
	if bytes.Contains(out, []byte(`"isError":true`)) {
		t.Fatalf("valid token rejected %s", out)
	}
}
func TestHTTPMCPCompleteOwnerSecrecy(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.login(t, "owner")
	secondCookie, secondCSRF := f.login(t, "second")
	ownerToken := f.issuer.sign(t, f.issuer.claims("owner", "mcp"))
	secondToken := f.issuer.sign(t, f.issuer.claims("second", "mcp"))
	created := f.http(t, cookie, csrf, "create_list", map[string]any{"name": "Fixture occasion", "date": time.Now().AddDate(0, 0, 2).Format("2006-01-02"), "groups": []string{"1"}}, 200)
	var list struct{ ID string }
	_ = json.Unmarshal(created, &list)
	args := map[string]any{"list": list.ID}
	raw := f.http(t, cookie, csrf, "add_request", map[string]any{"list": list.ID, "request": data.Request{Name: "Requested", Quantity: 1, Priority: 2}}, 200)
	var item struct{ ID string }
	_ = json.Unmarshal(raw, &item)
	for _, op := range []string{"lists", "detail", "deleted"} {
		a := args
		if op == "lists" {
			a = map[string]any{}
		}
		beforeHTTP := append([]byte(nil), f.http(t, cookie, csrf, op, a, 200)...)
		beforeMCP := append([]byte(nil), f.mcp(t, ownerToken, op, a, 200)...)
		if op == "lists" {
			f.mcp(t, secondToken, "claim", map[string]any{"list": list.ID, "item": item.ID}, 200)
		}
		f.http(t, secondCookie, secondCSRF, "add_surprise", map[string]any{"list": list.ID, "request": data.Request{Name: "HIDDEN-SURPRISE-" + op, Quantity: 1}}, 200)
		afterHTTP := f.http(t, cookie, csrf, op, a, 200)
		afterMCP := f.mcp(t, ownerToken, op, a, 200)
		if !bytes.Equal(beforeHTTP, afterHTTP) || !bytes.Equal(beforeMCP, afterMCP) {
			t.Fatalf("hidden activity changed complete %s response\n%s\n%s\n%s\n%s", op, beforeHTTP, afterHTTP, beforeMCP, afterMCP)
		}
		var rpc struct {
			Result struct{ Content []struct{ Text string } }
		}
		if json.Unmarshal(afterMCP, &rpc) != nil || len(rpc.Result.Content) != 1 || rpc.Result.Content[0].Text != string(afterHTTP) {
			t.Fatalf("HTTP/MCP views differ %s", afterMCP)
		}
	}
	detail := f.http(t, secondCookie, secondCSRF, "detail", args, 200)
	if !bytes.Contains(detail, []byte(`"BuyerName":"Owner"`)) && !bytes.Contains(detail, []byte(`"BuyerName":"Second"`)) {
		t.Fatal("member missing buyer")
	}
	for _, op := range []string{"delete_request", "restore_request"} {
		out := f.http(t, cookie, csrf, op, map[string]any{"list": list.ID, "item": item.ID}, 200)
		for _, hidden := range []string{"Buyer", "Surprise", "Creator", "HIDDEN"} {
			if bytes.Contains(out, []byte(hidden)) {
				t.Fatalf("%s leaked %s", op, out)
			}
		}
	}
	// Archive without changing owner metadata; both transports retain secrecy forever.
	f.advance.Store(true)
	archivedHTTP := f.http(t, cookie, csrf, "detail", args, 200)
	archivedMCP := f.mcp(t, ownerToken, "detail", args, 200)
	if !bytes.Contains(archivedHTTP, []byte(`"Archived":true`)) {
		t.Fatal("not archived")
	}
	for _, body := range [][]byte{archivedHTTP, archivedMCP} {
		for _, hidden := range []string{"Buyer", "Surprise", "Creator", "HIDDEN"} {
			if bytes.Contains(body, []byte(hidden)) {
				t.Fatalf("archive leaked %s", body)
			}
		}
	}
	f.http(t, cookie, csrf, "delete_request", map[string]any{"list": list.ID, "item": item.ID}, 409)
	denied := f.mcp(t, secondToken, "claim", map[string]any{"list": list.ID, "item": item.ID}, 200)
	if !bytes.Contains(denied, []byte("list is archived")) {
		t.Fatal("archive mutable over MCP")
	}
}

func TestBrowserFixture(t *testing.T) {
	if os.Getenv("UW_BROWSER_FIXTURE") != "1" {
		t.Skip("browser runner only")
	}
	f := newFixture(t)

	handler := f.s.Handler()
	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/advance" {
			f.advance.Store(true)
			w.WriteHeader(204)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer app.Close()
	f.s.cfg.Origin = app.URL
	f.s.oauth.RedirectURL = app.URL + "/auth/callback"
	fmt.Printf("FIXTURE_URL=%s\n", app.URL)
	<-time.After(4 * time.Minute)
}

func TestVerifiedGroupsRevocationAndAdminIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, e := f.store.SignIn(ctx, f.owner.Identity, "Owner", []string{"fixture-family"})
	if e != nil || !owner.Admin || len(owner.Groups) != 1 {
		t.Fatalf("owner mapping: %+v %v", owner, e)
	}
	_, e = f.store.SignIn(ctx, f.second.Identity, "Second", []string{"fixture-family"})
	if e != nil {
		t.Fatal(e)
	}
	list, e := f.store.CreateList(ctx, f.owner.Identity, "Shared", time.Now().AddDate(0, 0, 2).Format("2006-01-02"), []int64{1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.ListDetail(ctx, f.second.Identity, list.ID, data.Page{Limit: 100}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.SignIn(ctx, f.second.Identity, "Second", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.ListDetail(ctx, f.second.Identity, list.ID, data.Page{Limit: 100}); e != data.ErrMissing {
		t.Fatal("revoked membership retained access", e)
	}
	if _, e = f.store.SignIn(ctx, data.Identity{Issuer: "https://other.example.invalid", Subject: "owner"}, "Owner", []string{"fixture-family"}); e != data.ErrInvalid {
		t.Fatal("wrong issuer provisioned", e)
	}
	// Even a configured administrator who is not a shared-group member has no content bypass.
	if _, e = f.store.SignIn(ctx, f.owner.Identity, "Owner", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.SignIn(ctx, f.second.Identity, "Second", []string{"fixture-family"}); e != nil {
		t.Fatal(e)
	}
	secondList, e := f.store.CreateList(ctx, f.second.Identity, "Second's list", time.Now().AddDate(0, 0, 2).Format("2006-01-02"), []int64{1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.ListDetail(ctx, f.owner.Identity, secondList.ID, data.Page{Limit: 100}); e != data.ErrMissing {
		t.Fatal("admin content bypass", e)
	}
	if e = f.store.SetGroup(ctx, f.owner.Identity, 1, "Fixture family", true); e != nil {
		t.Fatal(e)
	}
	account, e := f.store.SignIn(ctx, f.second.Identity, "Second", []string{"fixture-family"})
	if e != nil || len(account.Groups) != 0 {
		t.Fatal("disabled group reactivated", e)
	}
}

func TestMultipleSharingGroupsAndClaimAdminMapping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	account, e := f.store.SignIn(ctx, f.owner.Identity, "Owner", []string{"fixture-family", "fixture-extended"})
	if e != nil || len(account.Groups) != 2 {
		t.Fatal("multiple group mapping failed", e)
	}
	_, e = f.store.SignIn(ctx, f.second.Identity, "Second", []string{"fixture-extended"})
	if e != nil {
		t.Fatal(e)
	}
	list, e := f.store.CreateList(ctx, f.owner.Identity, "Both groups", time.Now().AddDate(0, 0, 2).Format("2006-01-02"), []int64{1, 2})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.ListDetail(ctx, f.second.Identity, list.ID, data.Page{Limit: 100}); e != nil {
		t.Fatal("second shared group not authorized", e)
	}
	account, e = f.store.SignIn(ctx, f.second.Identity, "Second", []string{"fixture-admin"})
	if e != nil || !account.Admin {
		t.Fatal("claim admin mapping failed", e)
	}
	if _, e = f.store.Users(ctx, f.second.Identity); e != nil {
		t.Fatal("claim admin cannot manage metadata", e)
	}
	if _, e = f.store.ListDetail(ctx, f.second.Identity, list.ID, data.Page{Limit: 100}); e != data.ErrMissing {
		t.Fatal("claim admin bypassed content sharing", e)
	}
	account, e = f.store.SignIn(ctx, f.second.Identity, "Second", nil)
	if e != nil || account.Admin {
		t.Fatal("claim admin mapping not revoked", e)
	}
	if _, e = f.store.Users(ctx, f.second.Identity); e != data.ErrMissing {
		t.Fatal("revoked claim admin retained metadata access", e)
	}
}

func TestInsecureConfigurationRejected(t *testing.T) {
	f := newFixture(t)
	for _, change := range []func(*Config){
		func(c *Config) { c.Origin = "http://app.example.invalid" },
		func(c *Config) { c.Issuer = "http://issuer.example.invalid" },
		func(c *Config) { c.Origin += "/" },
		func(c *Config) { c.MCPAudience = c.ClientID },
		func(c *Config) { c.GroupsClaim = "" },
	} {
		c := f.s.cfg
		change(&c)
		if _, e := New(context.Background(), c, f.store); e == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}

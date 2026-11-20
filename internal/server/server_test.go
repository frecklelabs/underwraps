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
				q.Set("choose", "third")
				third := "/authorize?" + q.Encode()
				_, _ = fmt.Fprintf(w, `<a href="%s">Owner</a><a href="%s">Second</a><a href="%s">Third</a>`, owner, second, third)
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
	}, Issuer: f.server.URL, RequiredGroup: "fixture-family", AdministratorGroups: []string{"fixture-admin"}, Administrators: []data.Identity{{Issuer: f.server.URL, Subject: "owner"}}})
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
	ownerAccount, e := store.SignIn(context.Background(), owner.Identity, "Owner", owner.Groups)
	if e != nil {
		t.Fatal(e)
	}
	secondAccount, e := store.SignIn(context.Background(), second.Identity, "Second", second.Groups)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.SetUser(context.Background(), owner.Identity, ownerAccount.User.ID, "Owner", false, true, []int64{secondAccount.User.ID}); e != nil {
		t.Fatal(e)
	}
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
	if e = f.store.SetUser(context.Background(), f.owner.Identity, user.ID, "Owner", true, false, nil); e != nil {
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
	created := f.http(t, cookie, csrf, "create_list", map[string]any{"name": "Fixture occasion", "date": time.Now().AddDate(0, 0, 2).Format("2006-01-02")}, 200)
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

func TestRequiredGroupAndAdminIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, e := f.store.SignIn(ctx, f.second.Identity, "Second", nil); e != data.ErrMissing {
		t.Fatal("missing required group accepted", e)
	}
	account, e := f.store.SignIn(ctx, f.second.Identity, "Second", []string{"fixture-family", "fixture-admin"})
	if e != nil || !account.Admin {
		t.Fatal(account, e)
	}
	list, e := f.store.CreateList(ctx, f.second.Identity, "Private", time.Now().AddDate(0, 0, 2).Format("2006-01-02"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.ListDetail(ctx, f.owner.Identity, list.ID, data.Page{Limit: 100}); e != data.ErrMissing {
		t.Fatal("admin bypass", e)
	}
	account, e = f.store.SignIn(ctx, f.second.Identity, "Second", []string{"fixture-family"})
	if e != nil || account.Admin {
		t.Fatal("admin revocation", account, e)
	}
	if _, e = f.store.Users(ctx, f.second.Identity); e != data.ErrMissing {
		t.Fatal(e)
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

func TestSharingCallerScopedHTTPAndMCP(t *testing.T) {
	f := newFixture(t)
	ownerCookie, ownerCSRF := f.login(t, "owner")
	managerCookie, managerCSRF := f.login(t, "second")
	thirdCookie, thirdCSRF := f.login(t, "third")
	ownerToken := f.issuer.sign(t, f.issuer.claims("owner", "mcp"))
	managerToken := f.issuer.sign(t, f.issuer.claims("second", "mcp"))
	thirdToken := f.issuer.sign(t, f.issuer.claims("third", "mcp"))
	raw := f.http(t, ownerCookie, ownerCSRF, "create_list", map[string]any{"name": "Private", "date": time.Now().AddDate(0, 0, 2).Format("2006-01-02")}, 200)
	var list struct{ ID string }
	if e := json.Unmarshal(raw, &list); e != nil {
		t.Fatal(e)
	}
	people := f.http(t, thirdCookie, thirdCSRF, "people", map[string]any{}, 200)
	if bytes.Contains(people, []byte("Identity")) || bytes.Contains(people, []byte("Subject")) {
		t.Fatal("picker leaks identities")
	}
	var persons []struct{ ID, Name string }
	_ = json.Unmarshal(people, &persons)
	var second, third string
	for _, p := range persons {
		if p.Name == "Second" {
			second = p.ID
		}
		if p.Name == "Third" {
			third = p.ID
		}
	}
	if second == "" || third == "" {
		t.Fatal("missing people")
	}
	access := []map[string]string{{"user": second, "role": "manager"}, {"user": third, "role": "viewer"}}
	args := map[string]any{"list": list.ID, "access": access}
	f.http(t, thirdCookie, thirdCSRF, "set_sharing", args, 404)
	result := f.mcp(t, managerToken, "set_sharing", args, 200)
	if bytes.Contains(result, []byte(`"isError":true`)) {
		t.Fatal(string(result))
	}
	f.http(t, thirdCookie, thirdCSRF, "detail", map[string]any{"list": list.ID}, 200)
	f.http(t, thirdCookie, thirdCSRF, "set_sharing", args, 404)
	f.http(t, managerCookie, managerCSRF, "set_sharing", map[string]any{"list": list.ID, "access": access[:1]}, 200)
	for _, op := range []string{"detail", "sharing", "claim", "release_claim", "delete_surprise", "add_surprise", "update_request", "delete_request", "restore_request"} {
		var firstHTTP, firstMCP []byte
		for _, l := range []string{list.ID, "999999"} {
			args := map[string]any{"list": l, "item": "999"}
			if op == "add_surprise" || op == "update_request" {
				args["request"] = data.Request{Name: "Invalid URL", Link: "file:///invalid", Quantity: 1}
			}
			h := f.http(t, thirdCookie, thirdCSRF, op, args, 404)
			m := f.mcp(t, thirdToken, op, args, 200)
			if !bytes.Contains(m, []byte("not found")) {
				t.Fatal(string(m))
			}
			if firstHTTP != nil && (!bytes.Equal(firstHTTP, h) || !bytes.Equal(firstMCP, m)) {
				t.Fatal("missing/unauthorized transport errors differ", op)
			}
			firstHTTP = h
			firstMCP = m
		}
	}
	// Metadata admin cannot add themselves as a default manager over either transport.
	f.http(t, ownerCookie, ownerCSRF, "set_user", map[string]any{"user": third, "name": "Third", "default_managers": []string{"1"}}, 400)
	r := f.mcp(t, ownerToken, "set_user", map[string]any{"user": third, "name": "Third", "default_managers": []string{"1"}}, 200)
	if !bytes.Contains(r, []byte("invalid input")) {
		t.Fatal(string(r))
	}
}

func TestSetUserCannotDisableLastManager(t *testing.T) {
	f := newFixture(t)
	adminCookie, adminCSRF := f.login(t, "owner")
	managerCookie, managerCSRF := f.login(t, "second")
	childCookie, childCSRF := f.login(t, "third")
	adminToken := f.issuer.sign(t, f.issuer.claims("owner", "mcp"))
	managerToken := f.issuer.sign(t, f.issuer.claims("second", "mcp"))
	people := f.http(t, adminCookie, adminCSRF, "people", map[string]any{}, 200)
	var persons []struct{ ID, Name string }
	if err := json.Unmarshal(people, &persons); err != nil {
		t.Fatal(err)
	}
	var manager, child string
	for _, p := range persons {
		if p.Name == "Second" {
			manager = p.ID
		}
		if p.Name == "Third" {
			child = p.ID
		}
	}
	if manager == "" || child == "" {
		t.Fatal("missing people")
	}
	f.http(t, adminCookie, adminCSRF, "set_user", map[string]any{"user": child, "name": "Third", "default_managers": []string{manager}}, 200)
	raw := f.http(t, childCookie, childCSRF, "create_list", map[string]any{"name": "Private", "date": time.Now().AddDate(0, 0, 2).Format("2006-01-02")}, 200)
	var list struct{ ID string }
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"user": manager, "name": "Changed", "disabled": true}
	// A non-admin cannot change account settings; admin gets the same generic
	// validation error over either transport without learning any list content.
	f.http(t, managerCookie, managerCSRF, "set_user", args, 404)
	if r := f.mcp(t, managerToken, "set_user", args, 200); !bytes.Contains(r, []byte("not found")) {
		t.Fatal(string(r))
	}
	before := f.http(t, adminCookie, adminCSRF, "users", map[string]any{}, 200)
	f.http(t, adminCookie, adminCSRF, "set_user", args, 400)
	if r := f.mcp(t, adminToken, "set_user", args, 200); !bytes.Contains(r, []byte("invalid input")) {
		t.Fatal(string(r))
	}
	after := f.http(t, adminCookie, adminCSRF, "users", map[string]any{}, 200)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected account settings changed metadata")
	}
	f.http(t, managerCookie, managerCSRF, "set_sharing", map[string]any{"list": list.ID, "access": []map[string]string{{"user": manager, "role": "manager"}}}, 200)
	f.http(t, childCookie, childCSRF, "set_sharing", map[string]any{"list": list.ID}, 404)
	f.http(t, adminCookie, adminCSRF, "detail", map[string]any{"list": list.ID}, 404)
	if r := f.mcp(t, adminToken, "detail", map[string]any{"list": list.ID}, 200); !bytes.Contains(r, []byte("not found")) {
		t.Fatal(string(r))
	}
}

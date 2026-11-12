package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/frecklelabs/underwraps/internal/data"
	"golang.org/x/oauth2"
)

type Config struct {
	Origin       string
	Issuer       string
	ClientID     string
	ClientSecret string
	MCPAudience  string
	GroupsClaim  string
	Scopes       []string
}
type principal struct {
	Admin    bool
	Identity data.Identity
	Name     string
	Groups   []string
	Expiry   time.Time
}
type session struct {
	Principal principal
	CSRF      string
	Expiry    time.Time
}
type login struct {
	State, Nonce, Verifier string
	Expiry                 time.Time
}
type Server struct {
	httpClient                   *http.Client
	cfg                          Config
	store                        *data.Store
	oauth                        oauth2.Config
	browserVerifier, mcpVerifier *oidc.IDTokenVerifier
	mu                           sync.Mutex
	sessions                     map[string]session
	logins                       map[string]login
	clock                        func() time.Time
}

func New(ctx context.Context, c Config, store *data.Store) (*Server, error) {
	for _, raw := range []string{c.Origin, c.Issuer} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("HTTPS origin and issuer required")
		}
	}
	u, _ := url.Parse(c.Origin)
	if u.Path != "" || strings.HasSuffix(c.Origin, "/") || c.ClientID == "" || c.MCPAudience == "" || c.ClientID == c.MCPAudience || c.GroupsClaim == "" {
		return nil, errors.New("invalid authentication configuration")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	if supplied, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		clone := *supplied
		clone.Timeout = 10 * time.Second
		clone.CheckRedirect = client.CheckRedirect
		client = &clone
	}
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	// Discovery may not downgrade any security endpoint to plaintext.
	var metadata struct {
		JWKS string `json:"jwks_uri"`
	}
	if provider.Claims(&metadata) != nil {
		return nil, errors.New("invalid discovery metadata")
	}
	for _, raw := range []string{provider.Endpoint().AuthURL, provider.Endpoint().TokenURL, metadata.JWKS} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, errors.New("HTTPS OIDC endpoints required")
		}
	}
	// Only asymmetric signatures; never accept an unsigned or shared-secret caller token.
	verifier := func(aud string) *oidc.IDTokenVerifier {
		return provider.Verifier(&oidc.Config{ClientID: aud, SupportedSigningAlgs: []string{"RS256", "ES256", "EdDSA"}})
	}
	scopes := []string{oidc.ScopeOpenID, "profile"}
	scopes = append(scopes, c.Scopes...)
	return &Server{httpClient: client, cfg: c, store: store, oauth: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: c.Origin + "/auth/callback", Scopes: scopes}, browserVerifier: verifier(c.ClientID), mcpVerifier: verifier(c.MCPAudience), sessions: map[string]session{}, logins: map[string]login{}, clock: time.Now}, nil
}
func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func same(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func (s *Server) normalize(ctx context.Context, raw string, v *oidc.IDTokenVerifier) (principal, *oidc.IDToken, error) {
	t, err := v.Verify(ctx, raw)
	if err != nil {
		return principal{}, nil, errors.New("invalid identity")
	}
	if t.Issuer != s.cfg.Issuer || t.Subject == "" || strings.TrimSpace(t.Subject) != t.Subject || len(t.Subject) > 256 || !s.clock().Before(t.Expiry) {
		return principal{}, nil, errors.New("invalid identity")
	}
	var claims map[string]json.RawMessage
	if t.Claims(&claims) != nil {
		return principal{}, nil, errors.New("invalid claims")
	}
	var name string
	_ = json.Unmarshal(claims["name"], &name)
	if name == "" {
		name = "Member"
	}
	groups := []string{}
	if raw, ok := claims[s.cfg.GroupsClaim]; ok {
		if json.Unmarshal(raw, &groups) != nil {
			return principal{}, nil, errors.New("invalid groups")
		}
	}
	if len(groups) > 100 || len(name) > 200 {
		return principal{}, nil, errors.New("invalid claims")
	}
	for _, g := range groups {
		if g == "" || len(g) > 256 {
			return principal{}, nil, errors.New("invalid groups")
		}
	}
	return principal{Identity: data.Identity{Issuer: t.Issuer, Subject: t.Subject}, Name: name, Groups: groups, Expiry: t.Expiry}, t, nil
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) pruneLocked() {
	now := s.clock()
	for k, v := range s.sessions {
		if !now.Before(v.Expiry) {
			delete(s.sessions, k)
		}
	}
	for k, v := range s.logins {
		if !now.Before(v.Expiry) {
			delete(s.logins, k)
		}
	}
}
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	key := randomToken()
	flow := login{State: randomToken(), Nonce: randomToken(), Verifier: oauth2.GenerateVerifier(), Expiry: s.clock().Add(5 * time.Minute)}
	s.mu.Lock()
	s.pruneLocked()
	if len(s.logins) >= 1000 {
		s.mu.Unlock()
		w.WriteHeader(503)
		return
	}
	s.logins[key] = flow
	s.mu.Unlock()
	s.cookie(w, "__Host-uw-login", key, 300)
	http.Redirect(w, r, s.oauth.AuthCodeURL(flow.State, oidc.Nonce(flow.Nonce), oauth2.S256ChallengeOption(flow.Verifier)), http.StatusFound)
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	c, err := r.Cookie("__Host-uw-login")
	if err != nil {
		http.Error(w, "invalid login", 401)
		return
	}
	s.mu.Lock()
	flow, ok := s.logins[c.Value]
	delete(s.logins, c.Value)
	s.mu.Unlock()
	s.cookie(w, "__Host-uw-login", "", -1)
	if !ok || !s.clock().Before(flow.Expiry) || !same(flow.State, r.URL.Query().Get("state")) || r.URL.Query().Get("code") == "" || r.URL.Query().Get("error") != "" {
		http.Error(w, "invalid login", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	token, err := s.oauth.Exchange(oidc.ClientContext(ctx, s.httpClient), r.URL.Query().Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		http.Error(w, "invalid login", 401)
		return
	}
	raw, _ := token.Extra("id_token").(string)
	p, id, err := s.normalize(ctx, raw, s.browserVerifier)
	if err != nil || !same(id.Nonce, flow.Nonce) {
		http.Error(w, "invalid login", 401)
		return
	}
	if _, err = s.store.SignIn(ctx, p.Identity, p.Name, p.Groups); err != nil {
		http.Error(w, "access denied", 403)
		return
	}
	key := randomToken()
	expiry := s.clock().Add(time.Hour)
	if p.Expiry.Before(expiry) {
		expiry = p.Expiry
	}
	s.mu.Lock()
	s.pruneLocked()
	if len(s.sessions) >= 10000 {
		s.mu.Unlock()
		w.WriteHeader(503)
		return
	}
	s.sessions[key] = session{Principal: p, CSRF: randomToken(), Expiry: expiry}
	s.mu.Unlock()
	// Rotation discards the old session; cookies contain only an opaque random handle.
	if old, e := r.Cookie("__Host-uw-session"); e == nil {
		s.mu.Lock()
		delete(s.sessions, old.Value)
		s.mu.Unlock()
	}
	s.cookie(w, "__Host-uw-session", key, int(expiry.Sub(s.clock()).Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (s *Server) browser(r *http.Request) (session, bool) {
	c, err := r.Cookie("__Host-uw-session")
	if err != nil {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[c.Value]
	if !ok || !s.clock().Before(v.Expiry) {
		delete(s.sessions, c.Value)
		return session{}, false
	}
	return v, true
}
func (s *Server) caller(r *http.Request) (principal, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || len(values[0]) > 16384 {
		return principal{}, errors.New("bearer required")
	}
	raw := strings.TrimPrefix(values[0], "Bearer ")
	p, _, err := s.normalize(r.Context(), raw, s.mcpVerifier)
	return p, err
}

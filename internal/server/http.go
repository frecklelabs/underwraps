package server

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/frecklelabs/underwraps/internal/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(wire(v))
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", s.startLogin)
	mux.HandleFunc("/auth/callback", s.callback)
	mux.HandleFunc("/api/", s.api)
	mux.HandleFunc("/mcp", s.mcp)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, web.Index)
		case "/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, web.JS)
		case "/app.css":
			w.Header().Set("Content-Type", "text/css")
			_, _ = io.WriteString(w, web.CSS)
		case "/healthz":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		// The configured origin, never forwarded Host headers, defines browser security.
		if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.cfg.Origin {
			http.Error(w, "origin denied", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.browser(r)
	if !ok {
		send(w, 401, map[string]string{"error": "sign in required"})
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if r.Header.Get("Origin") != s.cfg.Origin || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		send(w, 403, map[string]string{"error": "CSRF rejected"})
		return
	}
	op := strings.TrimPrefix(r.URL.Path, "/api/")
	// The bootstrap is read-only. Its CSRF token is protected by the same-origin policy.
	if op != "me" && !same(sess.CSRF, r.Header.Get("X-CSRF-Token")) {
		send(w, 403, map[string]string{"error": "CSRF rejected"})
		return
	}
	account, err := s.store.SignIn(r.Context(), sess.Principal.Identity, sess.Principal.Name, sess.Principal.Groups)
	if err != nil {
		send(w, 403, map[string]string{"error": "access denied"})
		return
	}
	if op == "me" {
		send(w, 200, struct {
			Account any
			CSRF    string
		}{account, sess.CSRF})
		return
	}
	if op == "logout" {
		c, _ := r.Cookie("__Host-uw-session")
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
		s.cookie(w, "__Host-uw-session", "", -1)
		send(w, 200, struct{}{})
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		send(w, 400, map[string]string{"error": "invalid input"})
		return
	}
	var a arguments
	if decode(raw, &a) != nil {
		send(w, 400, map[string]string{"error": "invalid input"})
		return
	}
	p := sess.Principal
	p.Admin = account.Admin
	out, err := s.operate(r.Context(), p, op, a)
	if err != nil {
		code, message := publicError(err)
		send(w, code, map[string]string{"error": message})
		return
	}
	send(w, 200, out)
}
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	p, err := s.caller(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "invalid caller", 401)
		return
	}
	account, err := s.store.SignIn(r.Context(), p.Identity, p.Name, p.Groups)
	if err != nil {
		http.Error(w, "access denied", 403)
		return
	}
	p.Admin = account.Admin
	// One stateless server per authenticated request prevents identity/session reuse.
	server := mcp.NewServer(&mcp.Implementation{Name: "underwraps", Version: "1"}, nil)
	for _, op := range operationNames {
		server.AddTool(&mcp.Tool{Name: op, Description: "Caller-scoped wish list operation: " + op, InputSchema: toolSchema()}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var a arguments
			if decode(req.Params.Arguments, &a) != nil {
				return toolError("invalid input"), nil
			}
			out, err := s.operate(ctx, p, op, a)
			if err != nil {
				_, message := publicError(err)
				return toolError(message), nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(wire(out))}}}, nil
		})
	}
	mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}).ServeHTTP(w, r)
}
func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}
func toolSchema() any {
	// Strict decoding also rejects unknown identity arguments when a client skips schema validation.
	props := map[string]any{}
	for _, k := range []string{"list", "item", "name", "date", "user", "group"} {
		props[k] = map[string]string{"type": "string"}
	}
	for _, k := range []string{"archived", "disabled", "active"} {
		props[k] = map[string]string{"type": "boolean"}
	}
	for _, k := range []string{"offset", "limit"} {
		props[k] = map[string]string{"type": "integer"}
	}
	props["groups"] = map[string]any{"type": "array", "items": map[string]string{"type": "string"}}
	props["request"] = map[string]any{"type": "object"}
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

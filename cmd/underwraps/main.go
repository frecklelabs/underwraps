package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/frecklelabs/underwraps/internal/data"
	"github.com/frecklelabs/underwraps/internal/server"
)

func main() {
	path := flag.String("database", "", "private SQLite file (required)")
	zone := flag.String("timezone", "", "IANA calendar timezone (required)")
	listen := flag.String("listen", ":8080", "HTTP listener behind TLS ingress")
	flag.Parse()
	// Configuration is supplied privately. It never enters the embedded UI bundle.
	var groups []data.GroupMapping
	var admins []data.Identity
	var adminGroups []string
	var scopes []string
	for raw, dst := range map[string]any{"UW_GROUPS": &groups, "UW_ADMINS": &admins, "UW_ADMIN_GROUPS": &adminGroups, "UW_SCOPES": &scopes} {
		if v := os.Getenv(raw); v != "" {
			if json.Unmarshal([]byte(v), dst) != nil {
				log.Fatal("invalid " + raw)
			}
		}
	}
	issuer := os.Getenv("UW_ISSUER")
	store, err := data.Open(data.Config{Path: *path, Timezone: *zone, Clock: time.Now, Issuer: issuer, Groups: groups, Administrators: admins, AdministratorGroups: adminGroups})
	if err != nil {
		log.Fatal("database initialization failed")
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	app, err := server.New(ctx, server.Config{Origin: os.Getenv("UW_ORIGIN"), Issuer: issuer, ClientID: os.Getenv("UW_CLIENT_ID"), ClientSecret: os.Getenv("UW_CLIENT_SECRET"), MCPAudience: os.Getenv("UW_MCP_AUDIENCE"), GroupsClaim: os.Getenv("UW_GROUPS_CLAIM"), Scopes: scopes}, store)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	log.Print("HTTP and caller-scoped MCP ready")
	log.Fatal(srv.ListenAndServe())
}

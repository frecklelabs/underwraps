package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNegativeFixtures(t *testing.T) {
	for name, src := range map[string]string{
		"embed-opener":     `package bad;import "embed";var file embed.FS`,
		"checker-opener":   `package bad;import "github.com/frecklelabs/underwraps/internal/boundary";func read(){boundary.Check("/data")}`,
		"http-file":        `package bad;import h "net/http";var _=h.FileServer`,
		"sql":              "package bad;import q \"database/sql\";var _ *q.DB",
		"driver":           "package bad;import _ \"modernc.org/sqlite\"",
		"alternate-driver": "package bad;import _ \"github.com/mattn/go-sqlite3\"",
		"file":             "package bad;import o \"os\";var _=o.Open",
		"indirect-file":    "package bad;import o \"os\";var open=o.ReadFile",
		"syscall":          "package bad;import \"syscall\";var _=syscall.Open",
		"unknown":          "package bad;import _ \"example.invalid/query\"",
		"dot":              "package bad;import . \"os\";var _=Open",
		"build-tag":        "//go:build excluded\n\npackage bad;import _ \"database/sql\"",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "nested", "tool")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(src), 0600); err != nil {
				t.Fatal(err)
			}
			if Check(root) == nil {
				t.Fatal("violation accepted")
			}
		})
	}
	for _, src := range []string{"package data;import \"database/sql\";type Escape struct{ DB *sql.DB }", "package data;import \"database/sql\";func Raw()*sql.DB{return nil}", "package data;import \"database/sql\";type Escape = sql.DB"} {
		if inspect("internal/data/leak.go", []byte(src)) == nil {
			t.Fatal("export accepted")
		}
	}
}
func TestRepository(t *testing.T) {
	if err := Check("../.."); err != nil {
		t.Fatal(err)
	}
}

// Approved export names are deliberate: rejecting an unrelated name would not
// demonstrate that the actual capability escape is detected.
func TestTransitiveAPIFixtures(t *testing.T) {
	fixtures := map[string]struct {
		files map[string]string
		want  string
	}{
		"context-as-error-result":  {map[string]string{"a.go": `package data; import "context"; type error = context.Context; type Config struct{}; type Store struct{}; func Open(c Config) (*Store, error) { return nil, context.Background() }`}, "shadows approved signature type"},
		"error-interface-field":    {map[string]string{"a.go": `package data; type Store struct { Failure error }`}, "transitive type"},
		"error-interface-alias":    {map[string]string{"a.go": `package data; type View = error`}, "transitive type"},
		"error-interface-nested":   {map[string]string{"a.go": `package data; type carrier = error; type wrapper struct { Failure carrier }; type View struct { Value *wrapper }`}, "transitive type"},
		"context-field":            {map[string]string{"a.go": `package data; import "context"; type Store struct { Context context.Context }`}, "transitive type"},
		"context-exported-alias":   {map[string]string{"a.go": `package data; import "context"; type View = context.Context`}, "transitive type"},
		"context-defined-type":     {map[string]string{"a.go": `package data; import "context"; type View context.Context`}, "transitive type"},
		"context-cross-file-alias": {map[string]string{"a.go": `package data; import c "context"; type carrier = c.Context`, "b.go": `package data; type next = carrier; type Store struct { Context next }`}, "transitive type"},
		"context-nested":           {map[string]string{"a.go": `package data; import "context"; type wrapper struct { Context context.Context }; type Store struct { Values map[string][]*wrapper }`}, "transitive type"},
		"context-embedded":         {map[string]string{"a.go": `package data; import "context"; type Store struct { context.Context }`}, "transitive type"},
		"context-embedded-alias":   {map[string]string{"a.go": `package data; import "context"; type carrier = context.Context; type Store struct { carrier }`}, "transitive type"},
		"context-recursive":        {map[string]string{"a.go": `package data; import "context"; type node struct { Next *node; Context context.Context }; type View struct { Root *node }`}, "transitive type"},
		"context-return":           {map[string]string{"a.go": `package data; import "context"; type Store struct{}; func (s *Store) Close() context.Context { return nil }`}, "operation signature"},
		"alias-field":              {map[string]string{"a.go": `package data; import "database/sql"; type raw = sql.DB; type Store struct { DB *raw }`}, "transitive type"},
		"cross-file-alias":         {map[string]string{"a.go": `package data; import "database/sql"; type raw = sql.DB`, "b.go": `package data; type Store struct { DB *raw }`}, "transitive type"},
		"alias-chain":              {map[string]string{"a.go": `package data; import "database/sql"; type raw = sql.DB; type next = raw; type Store struct { DB *next }`}, "transitive type"},
		"defined-type":             {map[string]string{"a.go": `package data; import "database/sql"; type raw sql.DB; type Store struct { DB *raw }`}, "transitive type"},
		"embedded":                 {map[string]string{"a.go": `package data; import "database/sql"; type raw = sql.DB; type Store struct { *raw }`}, "transitive type"},
		"nested":                   {map[string]string{"a.go": `package data; import "database/sql"; type raw = sql.DB; type wrapper struct { DB *raw }; type Store struct { Values map[string][]*wrapper }`}, "transitive type"},
		"interface-field":          {map[string]string{"a.go": `package data; type executor interface { Exec(string) error }; type Store struct { DB executor }`}, "transitive type"},
		"embedded-interface":       {map[string]string{"a.go": `package data; type executor interface { Exec(string) error }; type Store struct { executor }`}, "transitive type"},
		"interface-return":         {map[string]string{"a.go": `package data; type executor interface { Exec(string) error }; func Open() executor { return nil }`}, "operation signature"},
		"alias-return":             {map[string]string{"a.go": `package data; import "database/sql"; type raw = sql.DB; func Open() *raw { return nil }`}, "operation signature"},
		"viewer-removed":           {map[string]string{"a.go": `package data; type Store struct{}; func(s *Store) Lists() error { return nil }`}, "operation signature"},
		"query-added":              {map[string]string{"a.go": `package data; type Store struct{}; func(s *Store) ListDetail(query string) error { return nil }`}, "operation signature"},
		"method-on-hidden-type":    {map[string]string{"a.go": `package data; type executor struct{}; func(s *executor) Open(query string) error { return nil }`}, "operation signature"},
		"inferred-handle":          {map[string]string{"a.go": `package data; import "database/sql"; var ErrMissing, _ = sql.Open("sqlite", "example.invalid")`}, "data value"},
		"func-field":               {map[string]string{"a.go": `package data; type Store struct { Query func(string) error }`}, "transitive type"},
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "internal", "data")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, src := range fixture.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := Check(root)
			if err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("want %q rejection, got %v", fixture.want, err)
			}
		})
	}
}

func TestParserOpenerRejected(t *testing.T) {
	for _, path := range []string{"app/main.go", "cmd/check-boundary/extra.go", "internal/data/nested/bad.go"} {
		err := inspect(path, []byte(`package bad; import "go/parser"; import "go/token"; func read(){ parser.ParseFile(token.NewFileSet(), "/data/app.sqlite", nil, 0) }`))
		if err == nil || !strings.Contains(err.Error(), "import go/parser outside data boundary") {
			t.Fatalf("%s: alternate opener accepted: %v", path, err)
		}
	}
}

func TestPrivateHandleAndSafeAliases(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "data")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	src := `package data; import "database/sql"; import "context"; type label = string; type node struct { Name label; Next *node }; type Store struct { db *sql.DB; ctx context.Context; Value node }`
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Check(root); err != nil {
		t.Fatal(err)
	}
}

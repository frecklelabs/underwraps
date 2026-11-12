// Package boundary checks every Go file, including tests, tools and build-tagged
// files. An import allowlist makes new persistence escape routes reviewable.
package boundary

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var allowed = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`bytes crypto/sha256 crypto/subtle encoding/base64 io net/url net/http/httptest crypto/rsa github.com/coreos/go-oidc/v3/oidc golang.org/x/oauth2 github.com/modelcontextprotocol/go-sdk/mcp github.com/go-jose/go-jose/v4 github.com/frecklelabs/underwraps/internal/server github.com/frecklelabs/underwraps/internal/web context crypto/rand encoding/binary encoding/json errors flag fmt io/fs log log/slog net/http os os/signal path/filepath slices sort strconv strings sync sync/atomic syscall testing time github.com/frecklelabs/underwraps/internal/data `) {
		allowed[p] = true
	}
}

// Check returns all violations; it never skips a Go file because of a build tag.
func Check(root string) error {
	var dataFiles []*ast.File
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "internal/data/") && !strings.Contains(strings.TrimPrefix(rel, "internal/data/"), "/") && !strings.HasSuffix(rel, "_test.go") {
			f, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.AllErrors)
			if err != nil {
				return err
			}
			dataFiles = append(dataFiles, f)
		}
		return inspect(rel, src)
	})
	if err != nil {
		return err
	}
	return checkAPI(dataFiles)
}
func inspect(path string, src []byte) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.AllErrors)
	if err != nil {
		return err
	}
	inData := strings.HasPrefix(path, "internal/data/") && !strings.Contains(strings.TrimPrefix(path, "internal/data/"), "/")
	tooling := strings.HasPrefix(path, "internal/boundary/") && !strings.Contains(strings.TrimPrefix(path, "internal/boundary/"), "/")
	imports := map[string]string{}
	for _, i := range f.Imports {
		p, err := strconv.Unquote(i.Path.Value)
		if err != nil {
			return err
		}
		name := filepath.Base(p)
		if i.Name != nil {
			name = i.Name.Name
		}
		imports[name] = p
		if !inData && p == "github.com/frecklelabs/underwraps/internal/boundary" && path != "cmd/check-boundary/main.go" {
			return fmt.Errorf("%s: checker import outside checker command", path)
		}
		permitted := (p == "embed" && path == "internal/web/assets.go") || allowed[p] || (p == "github.com/frecklelabs/underwraps/internal/boundary" && path == "cmd/check-boundary/main.go") || (tooling && (strings.HasPrefix(p, "go/") || p == "bytes"))
		if !inData && (!permitted || p == "syscall") {
			return fmt.Errorf("%s: import %s outside data boundary", path, p)
		}
		if !inData && (name == "." || name == "_") && !(path == "internal/web/assets.go" && p == "embed" && name == "_") {
			return fmt.Errorf("%s: anonymous imports outside data boundary", path)
		}
	}
	if inData {
		exports := map[string]bool{}
		for _, name := range strings.Fields(`Identity Config User Group GroupMapping Account SignIn Request OwnerItem ViewerItem List OwnerView MemberView View Summary Page Store ErrMissing ErrArchived ErrInvalid ErrConflict Open Close Register Users SetUser Groups CreateGroup SetGroup SetMembership CreateList UpdateList ListDetail DeletedItems Lists AddRequest AddSurprise UpdateRequest DeleteRequest RestoreRequest Claim ReleaseClaim DeleteSurprise`) {
			exports[name] = true
		}
		checkExport := func(name string) error {
			if ast.IsExported(name) && !exports[name] && !strings.HasSuffix(path, "_test.go") {
				return fmt.Errorf("%s: unreviewed data export %s", path, name)
			}
			return nil
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if e := checkExport(d.Name.Name); e != nil {
					return e
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch x := spec.(type) {
					case *ast.TypeSpec:
						if e := checkExport(x.Name.Name); e != nil {
							return e
						}
					case *ast.ValueSpec:
						for _, name := range x.Names {
							if e := checkExport(name.Name); e != nil {
								return e
							}
							if ast.IsExported(name.Name) && containsSQL(x.Type, imports) {
								return fmt.Errorf("%s: exported database value", path)
							}
						}
					}
				}
			}
		}
		// The package has no exported database-bearing fields or aliases.
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if t, ok := spec.(*ast.TypeSpec); ok && ast.IsExported(t.Name.Name) {
						if st, ok := t.Type.(*ast.StructType); ok {
							for _, field := range st.Fields.List {
								if len(field.Names) == 0 && containsSQL(field.Type, imports) {
									return fmt.Errorf("%s: embedded database field", path)
								}
								for _, name := range field.Names {
									if ast.IsExported(name.Name) && containsSQL(field.Type, imports) {
										return fmt.Errorf("%s: exported database field", path)
									}
								}
							}
						} else if containsSQL(t.Type, imports) {
							return fmt.Errorf("%s: exported database alias", path)
						}
					}
				}
			case *ast.FuncDecl:
				if ast.IsExported(d.Name.Name) && containsSQL(d.Type, imports) {
					return fmt.Errorf("%s: exported database operation", path)
				}
			}
		}
		return nil
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				p := imports[x.Name]
				if p == "net/http" {
					switch sel.Sel.Name {
					case "Dir", "FileServer", "FileServerFS", "ServeFile", "ServeFileFS", "NewFileTransport", "NewFileTransportFS":
						err = fmt.Errorf("%s: HTTP filesystem access outside data boundary", path)
					}
				}
				if p == "os" && !tooling {
					switch sel.Sel.Name {
					case "Getenv", "LookupEnv", "Exit", "Args", "Interrupt", "Stderr", "Stdout", "Stdin":
					default:
						err = fmt.Errorf("%s: filesystem access %s outside data boundary", path, sel.Sel.Name)
					}
				}
			}
		}
		return true
	})
	return err
}
func containsSQL(n ast.Node, imports map[string]string) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := s.X.(*ast.Ident); ok && imports[x.Name] == "database/sql" {
				found = true
			}
		}
		return true
	})
	return found
}

package boundary

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"strconv"
	"strings"
)

// Operation signatures are a reviewed contract: new methods, receiver changes,
// viewer removal, raw query parameters and interface returns require review here.
var operations = map[string]bool{
	`func (s *Store) AddRequest(ctx context.Context, v Identity, list int64, r Request) (OwnerItem, error)`:                  true,
	`func (s *Store) AddSurprise(ctx context.Context, v Identity, list int64, r Request) (int64, error)`:                     true,
	`func (s *Store) UpdateRequest(ctx context.Context, v Identity, list, id int64, r Request) (OwnerItem, error)`:           true,
	`func (s *Store) DeleteRequest(ctx context.Context, v Identity, list, id int64) (OwnerItem, error)`:                      true,
	`func (s *Store) RestoreRequest(ctx context.Context, v Identity, list, id int64) (OwnerItem, error)`:                     true,
	`func (s *Store) Claim(ctx context.Context, v Identity, list, item int64) error`:                                         true,
	`func (s *Store) ReleaseClaim(ctx context.Context, v Identity, list, item int64) error`:                                  true,
	`func (s *Store) DeleteSurprise(ctx context.Context, v Identity, list, item int64) error`:                                true,
	`func (s *Store) CreateList(ctx context.Context, v Identity, name, date string, groups []int64) (List, error)`:           true,
	`func (s *Store) UpdateList(ctx context.Context, v Identity, id int64, name, date string, groups []int64) (List, error)`: true,
	`func (s *Store) ListDetail(ctx context.Context, v Identity, id int64, p Page) (View, error)`:                            true,
	`func (s *Store) DeletedItems(ctx context.Context, v Identity, id int64, p Page) (OwnerView, error)`:                     true,
	`func (s *Store) Lists(ctx context.Context, v Identity, archived bool, p Page) ([]Summary, error)`:                       true,
	`func Open(c Config) (*Store, error)`: true,
	`func (s *Store) Close() error`:       true,
	`func (s *Store) Register(ctx context.Context, v Identity, name string) (User, error)`:                  true,
	`func (s *Store) Users(ctx context.Context, v Identity) ([]User, error)`:                                true,
	`func (s *Store) SetUser(ctx context.Context, v Identity, id int64, name string, disabled bool) error`:  true,
	`func (s *Store) Groups(ctx context.Context, v Identity) ([]Group, error)`:                              true,
	`func (s *Store) CreateGroup(ctx context.Context, v Identity, name string) (int64, error)`:              true,
	`func (s *Store) SetGroup(ctx context.Context, v Identity, id int64, name string, disabled bool) error`: true,
	`func (s *Store) SetMembership(ctx context.Context, v Identity, group, user int64, active bool) error`:  true,
}

type definition struct {
	expr    ast.Expr
	imports map[string]string
}

// Resolve the whole package, including aliases across files and build tags.
// A closed set of safe leaves fails closed for unknown or capability types.
func checkAPI(files []*ast.File) error {
	defs := map[string]definition{}
	for _, f := range files {
		imports := map[string]string{}
		for _, i := range f.Imports {
			p, _ := strconv.Unquote(i.Path.Value)
			parts := strings.Split(p, "/")
			name := parts[len(parts)-1]
			if i.Name != nil {
				name = i.Name.Name
			}
			imports[name] = p
		}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok {
				for _, s := range g.Specs {
					if t, ok := s.(*ast.TypeSpec); ok {
						// The signature allowlist uses these predeclared types.
						// Shadowing error with a context alias would otherwise
						// turn an approved error result into a capability return.
						switch t.Name.Name {
						case "error", "string", "int", "int64", "bool":
							return fmt.Errorf("data API shadows approved signature type %s", t.Name.Name)
						}
						if _, exists := defs[t.Name.Name]; exists {
							return fmt.Errorf("duplicate data type %s", t.Name.Name)
						}
						defs[t.Name.Name] = definition{t.Type, imports}
					}
				}
			}
		}
	}
	var safe func(ast.Expr, map[string]string, map[string]bool) bool
	safe = func(e ast.Expr, imports map[string]string, seen map[string]bool) bool {
		switch t := e.(type) {
		case *ast.Ident:
			if d, ok := defs[t.Name]; ok {
				if seen[t.Name] {
					return true
				}
				next := map[string]bool{}
				for k, v := range seen {
					next[k] = v
				}
				next[t.Name] = true
				return safe(d.expr, d.imports, next)
			}
			return t.Name == "string" || t.Name == "int" || t.Name == "int64" || t.Name == "bool"
		case *ast.StarExpr:
			return safe(t.X, imports, seen)
		case *ast.ArrayType:
			return safe(t.Elt, imports, seen)
		case *ast.MapType:
			return safe(t.Key, imports, seen) && safe(t.Value, imports, seen)
		case *ast.ParenExpr:
			return safe(t.X, imports, seen)
		case *ast.SelectorExpr:
			p, ok := t.X.(*ast.Ident)
			// Context carries arbitrary values through Value. It is permitted only
			// in the exact operation-input signatures above, never in a data graph.
			return ok && imports[p.Name] == "time" && t.Sel.Name == "Time"
		case *ast.StructType:
			for _, field := range t.Fields.List {
				exposed := len(field.Names) == 0
				for _, n := range field.Names {
					exposed = exposed || ast.IsExported(n.Name)
				}
				if !exposed {
					continue
				}
				// The injected clock is the only approved function-valued DTO field.
				if seen["Config"] && len(seen) == 1 && len(field.Names) == 1 && field.Names[0].Name == "Clock" {
					if fn, ok := field.Type.(*ast.FuncType); ok && fn.TypeParams == nil && len(fn.Params.List) == 0 && fn.Results != nil && len(fn.Results.List) == 1 && len(fn.Results.List[0].Names) == 0 {
						if sel, ok := fn.Results.List[0].Type.(*ast.SelectorExpr); ok {
							if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] == "time" && sel.Sel.Name == "Time" {
								continue
							}
						}
					}
				}
				if !safe(field.Type, imports, seen) {
					return false
				}
			}
			return true
		default:
			return false // Includes interfaces, funcs, channels, generics and any.
		}
	}
	for name, d := range defs {
		if ast.IsExported(name) && !safe(d.expr, d.imports, map[string]bool{name: true}) {
			return fmt.Errorf("data API %s exposes an unapproved transitive type", name)
		}
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(d.Name.Name) {
					continue
				}
				signature := *d
				signature.Body = nil
				signature.Doc = nil
				var buf bytes.Buffer
				if err := format.Node(&buf, token.NewFileSet(), &signature); err != nil {
					return err
				}
				if !operations[buf.String()] {
					return fmt.Errorf("unapproved data operation signature: %s", buf.String())
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if v, ok := s.(*ast.ValueSpec); ok {
						for _, n := range v.Names {
							if !ast.IsExported(n.Name) {
								continue
							}
							// Error sentinels are the only reviewed exported values. Do not permit
							// inference to turn an approved name into a handle or method value.
							if len(v.Names) != 1 || len(v.Values) != 1 || v.Type != nil || !strings.HasPrefix(n.Name, "Err") {
								return fmt.Errorf("unapproved data value %s", n.Name)
							}
							call, ok := v.Values[0].(*ast.CallExpr)
							if !ok || len(call.Args) != 1 {
								return fmt.Errorf("unapproved error sentinel")
							}
							sel, ok := call.Fun.(*ast.SelectorExpr)
							if !ok || sel.Sel.Name != "New" {
								return fmt.Errorf("unapproved error sentinel")
							}
							id, ok := sel.X.(*ast.Ident)
							if !ok || id.Name != "errors" {
								return fmt.Errorf("unapproved error sentinel")
							}
							lit, ok := call.Args[0].(*ast.BasicLit)
							if !ok || lit.Kind != token.STRING {
								return fmt.Errorf("unapproved error sentinel")
							}
							// Verify the import too, so a renamed SQL import cannot masquerade as errors.
							found := false
							for _, i := range f.Imports {
								p, _ := strconv.Unquote(i.Path.Value)
								if p == "errors" && (i.Name == nil || i.Name.Name == "errors") {
									found = true
								}
							}
							if !found {
								return fmt.Errorf("unapproved error sentinel import")
							}
						}
					}
				}
			}
		}
	}
	return nil
}

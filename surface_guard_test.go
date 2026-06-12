package smplkit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoInternalGeneratedTypeInPublicAPI locks principle P1 of the SDK
// Surface-Quality Standard: no public type a customer can reach may expose an
// internal/generated package on its signature. `go doc` (and IDE hover) render
// the static type, so a public alias/field/param/result that resolves into
// internal/generated leaks the implementation package onto the surface.
//
// This guard parses the wrapper source the same way the developer's tooling
// reads it — statically — and fails if any EXPORTED type/func/field/var
// references one of the generated import aliases in its signature.
func TestNoInternalGeneratedTypeInPublicAPI(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var violations []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// Collect import aliases that point at internal/generated/*.
		genAlias := map[string]bool{}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !strings.Contains(path, "/internal/generated/") {
				continue
			}
			if imp.Name != nil {
				genAlias[imp.Name.Name] = true
			} else {
				parts := strings.Split(path, "/")
				genAlias[parts[len(parts)-1]] = true
			}
		}
		if len(genAlias) == 0 {
			continue
		}

		refsGenerated := func(expr ast.Expr) bool {
			if expr == nil {
				return false
			}
			found := false
			ast.Inspect(expr, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && genAlias[id.Name] {
						found = true
						return false
					}
				}
				return true
			})
			return found
		}

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						if st, ok := s.Type.(*ast.StructType); ok {
							// go doc renders only exported fields; an unexported
							// field of a generated type is not on the surface.
							if st.Fields != nil {
								for _, f := range st.Fields.List {
									if fieldExported(f) && refsGenerated(f.Type) {
										violations = append(violations, name+": exported field on "+s.Name.Name)
									}
								}
							}
						} else if refsGenerated(s.Type) {
							// Alias or defined type: `type X = pkg.Y` / `type X pkg.Y`.
							violations = append(violations, name+": type "+s.Name.Name)
						}
					case *ast.ValueSpec:
						exported := false
						for _, n := range s.Names {
							if n.IsExported() {
								exported = true
							}
						}
						if exported && refsGenerated(s.Type) {
							violations = append(violations, name+": var/const "+s.Names[0].Name)
						}
					}
				}
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv != nil && !receiverExported(d.Recv) {
					continue // method on an unexported type — not public surface
				}
				if sig := d.Type; sig != nil {
					if sig.Params != nil {
						for _, f := range sig.Params.List {
							if refsGenerated(f.Type) {
								violations = append(violations, name+": "+funcLabel(d)+" (param)")
							}
						}
					}
					if sig.Results != nil {
						for _, f := range sig.Results.List {
							if refsGenerated(f.Type) {
								violations = append(violations, name+": "+funcLabel(d)+" (result)")
							}
						}
					}
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("public API exposes internal/generated types (P1 leak):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func fieldExported(f *ast.Field) bool {
	if len(f.Names) == 0 {
		return true // embedded field is part of the surface
	}
	for _, n := range f.Names {
		if n.IsExported() {
			return true
		}
	}
	return false
}

func receiverExported(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	t := recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.IsExported()
	}
	return false
}

func funcLabel(d *ast.FuncDecl) string {
	if d.Recv != nil && len(d.Recv.List) > 0 {
		t := d.Recv.List[0].Type
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		if id, ok := t.(*ast.Ident); ok {
			return id.Name + "." + d.Name.Name
		}
	}
	return d.Name.Name
}

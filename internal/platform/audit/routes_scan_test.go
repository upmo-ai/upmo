package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestRouteTableCoversTheSource parses every non-test Go file under internal,
// cmd, and pkg, finds the routes registered with POST, PUT, PATCH or DELETE, and
// fails when the route table and the source disagree.
func TestRouteTableCoversTheSource(t *testing.T) {
	registered := scanRegistrations(t, "../..", "../../../cmd", "../../../pkg")
	table := Routes()
	missing, gone := compare(registered, table)
	for _, pattern := range missing {
		t.Errorf("%s is registered in the source but missing from the route table", pattern)
	}
	for _, pattern := range gone {
		t.Errorf("%s is in the route table but no longer registered in the source", pattern)
	}
	for pattern, route := range table {
		if route.Action == "" {
			continue
		}
		if _, ok := catalog[route.Action]; !ok {
			t.Errorf("%s covers %s, which is not in the catalog", pattern, route.Action)
		}
	}
	for pattern := range sensitiveReads {
		if _, ok := table[pattern]; !ok {
			t.Errorf("%s is listed as a sensitive read but missing from the route table", pattern)
		}
	}
}

// TestRouteScanFailsOnPlantedRoute proves the ratchet bites: a route the table
// does not know about is reported, and so is a table entry the source dropped.
func TestRouteScanFailsOnPlantedRoute(t *testing.T) {
	dir := t.TempDir()
	src := "package api\n\nimport \"net/http\"\n\nfunc mount(mux *http.ServeMux) {\n" +
		"\tmux.HandleFunc(\"POST /api/planted\", nil)\n" +
		"\tmux.HandleFunc(\"GET /api/planted\", nil)\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "api.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	registered := scanRegistrations(t, dir)
	if !registered["POST /api/planted"] {
		t.Fatal("the scan missed the planted route")
	}
	if registered["GET /api/planted"] {
		t.Fatal("the scan covered a read-only route")
	}
	missing, gone := compare(registered, Routes())
	if len(missing) != 1 || missing[0] != "POST /api/planted" {
		t.Fatalf("missing = %v, want the planted route", missing)
	}
	if len(gone) == 0 {
		t.Fatal("an empty table diff cannot hold the real routes")
	}
}

// TestRouteScanResolvesRouteLoop proves a route registered in a loop over a
// map of constants keeps its method prefix.
func TestRouteScanResolvesRouteLoop(t *testing.T) {
	dir := t.TempDir()
	src := "package api\n\nimport \"net/http\"\n\nconst (\n\tPathA = \"/api/a\"\n\tPathB = \"/api/b\"\n)\n\nfunc mount(mux *http.ServeMux) {\n" +
		"\tfor path, name := range map[string]string{PathA: \"A\", PathB: \"B\"} {\n" +
		"\t\tmux.HandleFunc(\"POST \"+path, nil)\n" +
		"\t}\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "api.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	registered := scanRegistrations(t, dir)
	if !registered["POST /api/a"] || !registered["POST /api/b"] {
		t.Fatalf("registered = %v, want both loop routes with their method", registered)
	}
}

// sensitiveReads are read-only routes the table covers on purpose: the scan
// tracks changing methods, and these reads are guarded and audited like
// changes.
var sensitiveReads = map[string]bool{
	"GET /api/audit/events":          true,
	"GET /api/providers/{id}/models": true,
	"GET /api/providers/{id}/uses":   true,
}

// compare returns the source patterns the table lacks, and the table entries
// the source no longer registers, both sorted.
func compare(registered map[string]bool, table Table) (missing, gone []string) {
	for pattern := range registered {
		if _, ok := table[pattern]; !ok {
			missing = append(missing, pattern)
		}
	}
	for pattern := range table {
		if !registered[pattern] && !sensitiveReads[pattern] {
			gone = append(gone, pattern)
		}
	}
	sort.Strings(missing)
	sort.Strings(gone)
	return missing, gone
}

// changingMethods are the methods whose routes the audit covers.
var changingMethods = map[string]bool{"POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// scanRegistrations walks the roots and returns every changing route pattern
// registered on a mux. Patterns built from package constants are resolved; a
// route built over a range over a map of constants contributes all of them.
func scanRegistrations(t *testing.T, roots ...string) map[string]bool {
	t.Helper()
	registered := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			for pattern := range registrationsIn(t, path) {
				registered[pattern] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return registered
}

// registrationsIn parses one file and returns the changing patterns it
// registers on mux.Handle or mux.HandleFunc. A pattern it cannot resolve
// fails the scan.
func registrationsIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path) //nolint:gosec // the path comes from a walk of this module
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := constStrings(file)

	registered := map[string]bool{}
	// rangeKeys holds, per range variable, the map-literal keys it stands for.
	rangeKeys := map[string][]string{}
	var unresolved []token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.RangeStmt:
			// A route loop over a map literal registers every constant key,
			// under the name the range binds it to.
			lit, ok := node.X.(*ast.CompositeLit)
			if !ok {
				return true
			}
			key, ok := node.Key.(*ast.Ident)
			if !ok || key.Name == "_" {
				return true
			}
			for _, element := range lit.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if value, ok := literalString(pair.Key, consts); ok {
					rangeKeys[key.Name] = append(rangeKeys[key.Name], value)
				}
			}
		case *ast.CallExpr:
			call, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || (call.Sel.Name != "Handle" && call.Sel.Name != "HandleFunc") || len(node.Args) == 0 {
				return true
			}
			mux, ok := call.X.(*ast.Ident)
			if !ok || mux.Name != "mux" {
				return true
			}
			patterns, ok := routePattern(node.Args[0], consts, rangeKeys)
			if !ok {
				unresolved = append(unresolved, node.Args[0].Pos())
				return true
			}
			for _, pattern := range patterns {
				if method, _, found := strings.Cut(pattern, " "); found && changingMethods[method] {
					registered[pattern] = true
				}
			}
		}
		return true
	})
	for _, pos := range unresolved {
		t.Errorf("%s: the scan cannot resolve this route pattern; teach routePattern or add the route to the table", fset.Position(pos))
	}
	return registered
}

// routePattern resolves the first argument of a registration into every
// pattern it can stand for: a string literal "METHOD /path", or "METHOD "
// added to a package constant or to the keys of a map literal ranged over in
// a loop. Read-only patterns resolve too; the caller filters them out. It
// fails only when the pattern cannot be resolved at all.
func routePattern(expr ast.Expr, consts map[string]string, rangeKeys map[string][]string) ([]string, bool) {
	if lit, ok := expr.(*ast.BasicLit); ok {
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil, false
		}
		return []string{value}, true
	}
	binary, ok := expr.(*ast.BinaryExpr)
	if !ok || binary.Op != token.ADD {
		return nil, false
	}
	method, ok := literalString(binary.X, nil)
	if !ok {
		return nil, false
	}
	names, ok := patternNames(binary.Y, consts, rangeKeys)
	if !ok {
		return nil, false
	}
	patterns := make([]string, 0, len(names))
	for _, name := range names {
		patterns = append(patterns, method+name)
	}
	return patterns, true
}

// patternNames resolves the path half of a built pattern: a string literal or
// a package constant stands for one path, a range variable bound to the keys
// of a map literal for every key.
func patternNames(expr ast.Expr, consts map[string]string, rangeKeys map[string][]string) ([]string, bool) {
	if value, ok := literalString(expr, consts); ok {
		return []string{value}, true
	}
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return nil, false
	}
	keys, ok := rangeKeys[ident.Name]
	return keys, ok
}

// literalString reads a string literal or a package constant of one.
func literalString(expr ast.Expr, consts map[string]string) (string, bool) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(node.Value)
		return value, err == nil
	case *ast.Ident:
		value, ok := consts[node.Name]
		return value, ok
	default:
		return "", false
	}
}

// constStrings collects the package-level string constants of one file.
func constStrings(file *ast.File) map[string]string {
	consts := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			text, err := strconv.Unquote(lit.Value)
			if err == nil {
				consts[value.Names[0].Name] = text
			}
		}
		return true
	})
	return consts
}

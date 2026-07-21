// Package archtest checks the module import rules over a loaded package
// graph: contexts meet only through their facade, domain stays free of I/O and
// of its own app and adapters, and nothing imports cmd.
package archtest

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// NonContexts are the top-level packages under internal/ that are not
// bounded contexts.
var NonContexts = map[string]bool{"platform": true, "kernel": true, "archtest": true}

// IOPackages may not be imported by a domain package. A path also covers
// every package below it.
var IOPackages = []string{
	"database/sql",
	"net/http",
	"os",
	"os/exec",
	"github.com/jackc/pgx",
	"connectrpc.com/connect",
	"github.com/riverqueue/river",
}

// Violation is one forbidden import.
type Violation struct {
	From, To, Rule string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s imports %s: %s", v.From, v.To, v.Rule)
}

// Load loads every package of the module rooted at dir, test files included.
func Load(dir string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedImports | packages.NeedDeps,
		Dir:   dir,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		return nil, fmt.Errorf("archtest: %d package errors in %s", n, dir)
	}
	return pkgs, nil
}

// location is where a package sits in the module layout.
type location struct {
	cmd     bool
	context string // bounded context name, empty if none
	layer   string // first path element below the context, empty for the facade
}

func locate(module, path string) (location, bool) {
	rel, ok := strings.CutPrefix(path, module+"/")
	if !ok {
		return location{}, false
	}
	if rel == "cmd" || strings.HasPrefix(rel, "cmd/") {
		return location{cmd: true}, true
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 || parts[0] != "internal" || NonContexts[parts[1]] {
		return location{}, true
	}
	loc := location{context: parts[1]}
	if len(parts) > 2 {
		loc.layer = parts[2]
	}
	return loc, true
}

func isIO(path string) bool {
	for _, p := range IOPackages {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// Check returns every import in pkgs, and in the packages they reach, that
// breaks the import rules for the given module path.
func Check(module string, pkgs []*packages.Package) []Violation {
	seen := map[Violation]bool{}
	add := func(v Violation) { seen[v] = true }

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		from, ok := locate(module, p.PkgPath)
		if !ok {
			return
		}
		for path := range p.Imports {
			if isIO(path) && from.layer == "domain" {
				add(Violation{p.PkgPath, path, "domain imports an I/O package"})
			}
			to, ok := locate(module, path)
			if !ok {
				continue
			}
			if to.cmd {
				add(Violation{p.PkgPath, path, "cmd is not importable"})
			}
			if to.context != "" && to.layer != "" && from.context != to.context {
				add(Violation{p.PkgPath, path, "only the facade of context " + to.context + " is importable"})
			}
		}
		if from.layer == "domain" {
			for _, path := range reach(module, p) {
				to, _ := locate(module, path)
				if to.context == from.context && (to.layer == "app" || to.layer == "adapters") {
					add(Violation{p.PkgPath, path, "domain reaches " + to.layer})
				}
			}
		}
	})

	out := make([]Violation, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// reach returns the module packages p imports, directly or transitively.
func reach(module string, p *packages.Package) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*packages.Package)
	walk = func(q *packages.Package) {
		for path, imp := range q.Imports {
			if seen[path] || !strings.HasPrefix(path, module+"/") {
				continue
			}
			seen[path] = true
			out = append(out, path)
			walk(imp)
		}
	}
	walk(p)
	return out
}

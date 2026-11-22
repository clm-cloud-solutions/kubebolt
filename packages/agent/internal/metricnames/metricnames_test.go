// Package metricnames holds one guard: every metric name the agent emits or
// asks a Prometheus for is core in the shared catalog
// (packages/shared/metriccatalog).
//
// The API counts series by exact name: a name the catalog does not know is
// unknown, and once the ingest doors check the catalog it is dropped. So a new
// agent metric that nobody added to the catalog would be thrown away from every
// install the day it ships. This test fails first.
package metricnames

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/packages/agent/internal/promread"
	"github.com/kubebolt/kubebolt/packages/shared/metriccatalog"
)

// emittedNames walks the agent's source and returns every metric name written
// as a literal where a sample is built: the first argument of sample() and
// newSample(), a `MetricName: "…"` field, and the keys of the cAdvisor
// allowlist. Names built at run time (node_pressure_<resource>_…) are listed in
// the catalog by hand and checked by TestAComputedNameIsListed.
func emittedNames(t *testing.T) map[string]token.Position {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]token.Position{}
	add := func(e ast.Expr) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || s == "" {
			return
		}
		out[s] = fset.Position(lit.Pos())
	}
	for _, root := range []string{"..", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "sample" || id.Name == "newSample") && len(x.Args) > 0 {
						add(x.Args[0])
					}
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok && id.Name == "MetricName" {
						add(x.Value)
					}
				case *ast.ValueSpec:
					for i, name := range x.Names {
						if name.Name != "allowedCadvisorMetrics" || i >= len(x.Values) {
							continue
						}
						if cl, ok := x.Values[i].(*ast.CompositeLit); ok {
							for _, el := range cl.Elts {
								if kv, ok := el.(*ast.KeyValueExpr); ok {
									add(kv.Key)
								}
							}
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestEveryNameTheAgentEmitsIsCore(t *testing.T) {
	names := emittedNames(t)
	if len(names) < 40 {
		t.Fatalf("found only %d emitted names: the walk missed the collectors", len(names))
	}
	for name, pos := range names {
		if !metriccatalog.IsCore(name) {
			t.Errorf("%s (emitted at %s) is not core in packages/shared/metriccatalog: add it to agentNames", name, pos)
		}
	}
}

func TestEveryNamePromreadAsksForIsCore(t *testing.T) {
	for _, list := range [][]string{promread.DefaultMatchers, promread.CostMatchers} {
		for _, name := range list {
			if !metriccatalog.IsCore(name) {
				t.Errorf("promread asks for %s, which is not core in packages/shared/metriccatalog", name)
			}
		}
	}
}

// The node stress collector builds node_pressure_<resource>_waiting_seconds_total
// at run time, so the walk cannot see it.
func TestAComputedNameIsListed(t *testing.T) {
	for _, r := range []string{"cpu", "io", "memory"} {
		name := "node_pressure_" + r + "_waiting_seconds_total"
		if !metriccatalog.IsCore(name) {
			t.Errorf("%s is emitted by the node stress collector but is not core", name)
		}
	}
}

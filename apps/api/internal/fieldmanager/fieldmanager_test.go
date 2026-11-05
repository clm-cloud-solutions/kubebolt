package fieldmanager

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const metaV1 = "k8s.io/apimachinery/pkg/apis/meta/v1"

// writeOptions are the option types of every Kubernetes create, update, patch
// and apply.
var writeOptions = map[string]bool{
	"CreateOptions": true,
	"UpdateOptions": true,
	"PatchOptions":  true,
	"ApplyOptions":  true,
}

// TestEveryWriteNamesKubeBolt fails on a write option literal, anywhere in the
// API, that does not set FieldManager: that write would be recorded under the
// binary's name instead of KubeBolt's.
func TestEveryWriteNamesKubeBolt(t *testing.T) {
	var missing []string
	fset := token.NewFileSet()
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
			alias := ""
			for _, imp := range f.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); p == metaV1 {
					alias = "v1"
					if imp.Name != nil {
						alias = imp.Name.Name
					}
				}
			}
			if alias == "" {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || !writeOptions[sel.Sel.Name] {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != alias {
					return true
				}
				for _, e := range lit.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "FieldManager" {
							return true
						}
					}
				}
				missing = append(missing, fset.Position(lit.Pos()).String())
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range missing {
		t.Errorf("%s: a Kubernetes write without FieldManager: fieldmanager.Name", m)
	}
}

// TestForNamesTheActor: each of KubeBolt's actors writes under its own name,
// and a context nobody stamped is a person's (finding #69).
func TestForNamesTheActor(t *testing.T) {
	cases := []struct {
		ctx  context.Context
		want string
	}{
		{context.Background(), "kubebolt"},
		{WithActor(context.Background(), ActorPerson), "kubebolt"},
		{WithActor(context.Background(), ActorKobi), "kubebolt-kobi"},
		{WithActor(context.Background(), ActorAutopilot), "kubebolt-autopilot"},
		{WithActor(context.Background(), Actor("someone-else")), "kubebolt"},
	}
	for _, c := range cases {
		if got := For(c.ctx); got != c.want {
			t.Errorf("For(%q) = %q, want %q", ActorFrom(c.ctx), got, c.want)
		}
	}
}

// kubeboltOwnWrites are the files whose writes are KubeBolt's own, never a
// person's or Kobi's: boot, access reviews, agent installs. Everywhere else a
// write passes the request's context to For, or the actor that made it is lost.
// Paths as the walk sees them, from internal/ and from cmd/.
var kubeboltOwnWrites = []string{
	"/cmd/server/main.go",
	"/cluster/permissions.go",
	"/auth/agent_auth_tokenreview.go",
	"/api/agent_integration.go",
	"/integrations/agent_install.go",
}

// TestActionWritesNameTheirActor fails on `FieldManager: fieldmanager.Name`
// outside kubeboltOwnWrites (finding #69).
func TestActionWritesNameTheirActor(t *testing.T) {
	fset := token.NewFileSet()
	for _, root := range []string{"..", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			slash := filepath.ToSlash(path)
			for _, own := range kubeboltOwnWrites {
				if strings.HasSuffix(slash, own) {
					return nil
				}
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				kv, ok := n.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "FieldManager" {
					return true
				}
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok && sel.Sel.Name == "Name" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "fieldmanager" {
						t.Errorf("%s: write under fieldmanager.Name; pass the request's context to fieldmanager.For", fset.Position(kv.Pos()))
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
}

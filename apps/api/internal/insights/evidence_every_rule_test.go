package insights

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Every rule records what it saw (complete 2026-10-11). This reads
// the rules' source: a newInsight call with only its five prose arguments is a
// rule that discards its facts again, and it fails here instead of shipping.
func TestEveryInsightConstructorPassesEvidence(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "rules.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "newInsight" {
			return true
		}
		calls++
		if len(call.Args) <= 5 {
			t.Errorf("%s: newInsight without evidence", fset.Position(call.Pos()))
		}
		return true
	})
	if calls < 20 {
		t.Fatalf("found %d newInsight calls in rules.go — did the rules move?", calls)
	}
}

package testsupport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestTestsExplainBehavior prevents unexplained tests from entering the Go suite.
func TestTestsExplainBehavior(t *testing.T) {
	for _, dir := range []string{"../../cmd", "../../internal", "../../tests"} {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				if fn.Doc == nil || strings.TrimSpace(fn.Doc.Text()) == "" {
					t.Errorf("%s: %s has no explanatory comment", path, fn.Name.Name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect tests under %s: %v", dir, err)
		}
	}
}

package media

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestMediaHelpersUseRunner(t *testing.T) {
	violations, err := findDirectExecUses(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("media helpers must use Runner instead of os/exec:\n%s", strings.Join(violations, "\n"))
	}
}

func TestDirectExecPolicyCatchesHelperBypassingRunner(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "exec.go"), []byte(`package media

import (
	"context"
	"os/exec"
)

func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(`package media

import (
	"context"
	"os/exec"
)

func AudioDuration(ctx context.Context, path string) ([]byte, error) {
	return exec.CommandContext(ctx, "afinfo", path).Output()
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	violations, err := findDirectExecUses(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 2 {
		t.Fatalf("want import and call violations, got %v", violations)
	}
	if !hasViolation(violations, "probe.go", "imports os/exec") {
		t.Fatalf("missing import violation: %v", violations)
	}
	if !hasViolation(violations, "probe.go", "calls exec.CommandContext") {
		t.Fatalf("missing call violation: %v", violations)
	}
}

func hasViolation(violations []string, parts ...string) bool {
	for _, violation := range violations {
		matched := true
		for _, part := range parts {
			if !strings.Contains(violation, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func findDirectExecUses(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "exec.go" {
			continue
		}

		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}

		execAliases := map[string]bool{}
		for _, spec := range file.Imports {
			if spec.Path.Value != "\"os/exec\"" {
				continue
			}
			violations = append(violations, name+": imports os/exec")
			alias := "exec"
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias != "." && alias != "_" {
				execAliases[alias] = true
			}
		}
		if len(execAliases) == 0 {
			continue
		}

		file, err = parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Command" && selector.Sel.Name != "CommandContext") {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && execAliases[pkg.Name] {
				violations = append(violations, name+": calls "+pkg.Name+"."+selector.Sel.Name)
			}
			return true
		})
	}

	sort.Strings(violations)
	return violations, nil
}

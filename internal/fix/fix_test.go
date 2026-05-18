package fix

import (
	"go/token"
	"os"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/packages/packagestest"

	"github.com/fmenezes/unexported/internal/analyzer"
)

func loadFixTestPackages(t *testing.T, modules []packagestest.Module) ([]*packages.Package, func()) {
	t.Helper()
	export := packagestest.Export(t, packagestest.Modules, modules)

	cfg := export.Config
	cfg.Mode = packages.NeedName | packages.NeedFiles | packages.NeedTypes |
		packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedDeps | packages.NeedImports
	cfg.Tests = true

	seen := map[string]bool{}
	var patterns []string
	for _, m := range modules {
		if !seen[m.Name] {
			seen[m.Name] = true
			patterns = append(patterns, m.Name+"/...")
		}
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	return pkgs, export.Cleanup
}

func TestUnexportName(t *testing.T) {
	cases := []struct{ in, out string }{
		{"Foo", "foo"},
		{"FooBar", "fooBar"},
		{"foo", "foo"},
		{"", ""},
		{"ABC", "aBC"},
	}
	for _, tc := range cases {
		if got := unexportName(tc.in); got != tc.out {
			t.Errorf("unexportName(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

// Test: ApplyFixes renames exported symbol and its call site.
func TestApplyFixesRenamesDefinitionAndUsage(t *testing.T) {
	pkgs, cleanup := loadFixTestPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
func caller() { Exported() }
`,
		},
	}})
	t.Cleanup(cleanup)

	findings := analyzer.DedupeDetailedFindings(analyzer.AnalyzeDetailed(pkgs, analyzer.Options{}))
	if len(findings) == 0 {
		t.Fatal("expected at least one finding before applying fixes")
	}

	applied, skipped, err := ApplyFixes(pkgs, findings)
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}

	content, err := os.ReadFile(findings[0].Pos.Filename)
	if err != nil {
		t.Fatalf("reading modified file: %v", err)
	}
	src := string(content)
	if strings.Contains(src, "Exported") {
		t.Errorf("file still contains 'Exported' after fix:\n%s", src)
	}
	if !strings.Contains(src, "func exported()") {
		t.Errorf("expected renamed definition 'func exported()' in file:\n%s", src)
	}
	if !strings.Contains(src, "exported()") {
		t.Errorf("expected renamed call site 'exported()' in file:\n%s", src)
	}
}

// Test: ApplyFixes skips rename when the unexported name already exists in scope.
func TestApplyFixesSkipsOnNamingConflict(t *testing.T) {
	pkgs, cleanup := loadFixTestPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
func exported() {} // conflict: lowercase name already taken
`,
		},
	}})
	t.Cleanup(cleanup)

	findings := analyzer.DedupeDetailedFindings(analyzer.AnalyzeDetailed(pkgs, analyzer.Options{}))

	applied, skipped, err := ApplyFixes(pkgs, findings)
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (conflict should block rename)", applied)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

// Test: ApplyFixes renames a type and all its in-package references.
func TestApplyFixesRenamesType(t *testing.T) {
	pkgs, cleanup := loadFixTestPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type MyType struct{}

func newMyType() MyType { return MyType{} }
`,
		},
	}})
	t.Cleanup(cleanup)

	findings := analyzer.DedupeDetailedFindings(analyzer.AnalyzeDetailed(pkgs, analyzer.Options{}))

	applied, _, err := ApplyFixes(pkgs, findings)
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if applied == 0 {
		t.Fatal("expected at least one rename")
	}

	content, err := os.ReadFile(findings[0].Pos.Filename)
	if err != nil {
		t.Fatalf("reading modified file: %v", err)
	}
	src := string(content)
	if strings.Contains(src, "type MyType") {
		t.Errorf("file still contains 'type MyType' after fix:\n%s", src)
	}
	if !strings.Contains(src, "type myType") {
		t.Errorf("expected 'type myType' in file after fix:\n%s", src)
	}
}

// Test: ApplyFixes skips a finding whose Obj is nil.
func TestApplyFixesSkipsNilObj(t *testing.T) {
	f := analyzer.DetailedFinding{
		Finding: analyzer.Finding{Pos: token.Position{Filename: "a.go", Line: 1}, ObjName: "Foo", PkgName: "pkg"},
		Obj:     nil,
	}

	applied, skipped, err := ApplyFixes(nil, []analyzer.DetailedFinding{f})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if applied != 0 || skipped != 0 {
		t.Errorf("applied=%d skipped=%d, want both 0", applied, skipped)
	}
}

// Test: ApplyFixes returns an error when the source file cannot be read.
func TestApplyFixesReadError(t *testing.T) {
	pkgs, cleanup := loadFixTestPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
func caller() { Exported() }
`,
		},
	}})
	t.Cleanup(cleanup)

	findings := analyzer.DedupeDetailedFindings(analyzer.AnalyzeDetailed(pkgs, analyzer.Options{}))
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}

	if err := os.Remove(findings[0].Pos.Filename); err != nil {
		t.Fatalf("removing source file: %v", err)
	}

	_, _, err := ApplyFixes(pkgs, findings)
	if err == nil {
		t.Error("expected error when source file is missing, got nil")
	}
}

// Test: ApplyFixes returns an error when the source file cannot be written.
func TestApplyFixesWriteError(t *testing.T) {
	pkgs, cleanup := loadFixTestPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
func caller() { Exported() }
`,
		},
	}})
	t.Cleanup(cleanup)

	findings := analyzer.DedupeDetailedFindings(analyzer.AnalyzeDetailed(pkgs, analyzer.Options{}))
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}

	filename := findings[0].Pos.Filename
	if err := os.Chmod(filename, 0444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(filename, 0644) })

	_, _, err := ApplyFixes(pkgs, findings)
	if err == nil {
		t.Error("expected error when source file is read-only, got nil")
	}
}

// Test: ApplyFixes renames an exported field on an exported struct.
// Struct fields have nil Parent scope, exercising the hasConflict nil-parent guard.
func TestApplyFixesRenamesStructField(t *testing.T) {
	pkgs, cleanup := loadFixTestPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type Config struct {
	Verbose bool
}

func newConfig() Config { return Config{Verbose: true} }
`,
		},
	}})
	t.Cleanup(cleanup)

	findings := analyzer.DedupeDetailedFindings(analyzer.AnalyzeDetailed(pkgs, analyzer.Options{}))

	applied, _, err := ApplyFixes(pkgs, findings)
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if applied == 0 {
		t.Error("expected at least one rename (Config or Verbose)")
	}
}

// Test: ApplyFixes is a no-op when findings list is empty.
func TestApplyFixesNoFindings(t *testing.T) {
	pkgs, cleanup := loadFixTestPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func unexported() {}
`,
		},
	}})
	t.Cleanup(cleanup)

	applied, skipped, err := ApplyFixes(pkgs, nil)
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if applied != 0 || skipped != 0 {
		t.Errorf("applied = %d, skipped = %d, want both 0", applied, skipped)
	}
}

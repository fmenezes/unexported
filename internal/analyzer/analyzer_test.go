package analyzer_test

import (
	"go/token"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/packages/packagestest"

	"github.com/fmenezes/unexported/internal/analyzer"
)

func loadPackages(t *testing.T, modules []packagestest.Module) []*packages.Package {
	t.Helper()
	export := packagestest.Export(t, packagestest.Modules, modules)
	t.Cleanup(export.Cleanup)

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
	return pkgs
}

func findingNames(findings []analyzer.Finding) []string {
	names := make([]string, len(findings))
	for i, f := range findings {
		names[i] = f.ObjName
	}
	return names
}

func containsFinding(findings []analyzer.Finding, name string) bool {
	for _, f := range findings {
		if f.ObjName == name {
			return true
		}
	}
	return false
}

// Test: exported func used from another package → no finding
func TestExportedUsedExternally(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
`,
			"pkgb/b.go": `package pkgb

import "example.com/m/pkga"

func Caller() { pkga.Exported() }
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Exported") {
		t.Errorf("did not expect finding for Exported (used by pkgb), got %v", findingNames(findings))
	}
}

// Test: exported func used only inside its own package → finding
func TestExportedOnlyUsedInternally(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
func caller() { Exported() }
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if !containsFinding(findings, "Exported") {
		t.Errorf("expected finding for Exported, got %v", findingNames(findings))
	}
}

// Test: unexported func → no finding
func TestUnexportedNoFinding(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func unexported() {}
func caller() { unexported() }
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "unexported") {
		t.Errorf("did not expect finding for unexported, got %v", findingNames(findings))
	}
}

// Test: exported func used only in _test.go of another package → no finding
func TestExportedUsedInExternalTestFile(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
`,
			"pkgb/b_test.go": `package pkgb_test

import "example.com/m/pkga"
import "testing"

func TestSomething(t *testing.T) { pkga.Exported() }
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Exported") {
		t.Errorf("did not expect finding for Exported (used in external test), got %v", findingNames(findings))
	}
}

// Test: exported interface type used externally, its method not separately used → no finding on method
func TestInterfaceMethodNotFlaggedWhenInterfaceUsedExternally(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type Doer interface {
	Do()
}
`,
			"pkgb/b.go": `package pkgb

import "example.com/m/pkga"

func Accept(d pkga.Doer) { d.Do() }
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Do") {
		t.Errorf("did not expect finding for Do (method of externally-used interface Doer), got %v", findingNames(findings))
	}
	if containsFinding(findings, "Doer") {
		t.Errorf("did not expect finding for Doer (used externally by pkgb), got %v", findingNames(findings))
	}
}

// Test: exported interface never used externally → finding on interface
func TestInterfaceOnlyUsedInternally(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type Doer interface {
	Do()
}

func caller(d Doer) { d.Do() }
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if !containsFinding(findings, "Doer") {
		t.Errorf("expected finding for Doer (only used internally), got %v", findingNames(findings))
	}
}

// Test: struct with one field used externally and one not → finding only on unused field
func TestStructFieldPartialUsage(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type Config struct {
	Used   string
	Unused string
}
`,
			"pkgb/b.go": `package pkgb

import "example.com/m/pkga"

func F(c pkga.Config) string { return c.Used }
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Used") {
		t.Errorf("did not expect finding for Used (accessed externally), got %v", findingNames(findings))
	}
	if !containsFinding(findings, "Unused") {
		t.Errorf("expected finding for Unused (never accessed externally), got %v", findingNames(findings))
	}
}

// Test: //nolint:unexported suppresses finding
func TestNolintUnexported(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {} //nolint:unexported
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Exported") {
		t.Errorf("did not expect finding for Exported (has nolint comment), got %v", findingNames(findings))
	}
}

// Test: bare //nolint suppresses finding
func TestNolintBare(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {} //nolint
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Exported") {
		t.Errorf("did not expect finding for Exported (has bare nolint comment), got %v", findingNames(findings))
	}
}

// Test: //nolint:unexported on line above suppresses finding
func TestNolintLineAbove(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

//nolint:unexported
func Exported() {}
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Exported") {
		t.Errorf("did not expect finding for Exported (has nolint above), got %v", findingNames(findings))
	}
}

// Test: //nolint at top of file suppresses all findings in that file
func TestNolintFileLevel(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

//nolint:unexported

func ExportedA() {}
func ExportedB() {}
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "ExportedA") {
		t.Errorf("did not expect finding for ExportedA (file-level nolint), got %v", findingNames(findings))
	}
	if containsFinding(findings, "ExportedB") {
		t.Errorf("did not expect finding for ExportedB (file-level nolint), got %v", findingNames(findings))
	}
}

// Test: -exclude skips packages matching the prefix
func TestExcludeOption(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{Exclude: []string{"example.com/m/pkga"}})
	if containsFinding(findings, "Exported") {
		t.Errorf("did not expect finding for Exported (package excluded), got %v", findingNames(findings))
	}
}

// Test: interface type referenced externally but method never directly called → no finding on method
// (exercises the interfaceOf path where the method itself has no external callers)
func TestInterfaceMethodProtectedByExternalTypeReference(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type Doer interface {
	Do()
}
`,
			"pkgb/b.go": `package pkgb

import "example.com/m/pkga"

func Accept(d pkga.Doer) {}
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "Do") {
		t.Errorf("did not expect finding for Do (interface Doer is externally referenced even though Do is never directly called), got %v", findingNames(findings))
	}
}

// Test: //nolint in a file with no declarations → no panic, no findings
func TestNolintFileWithNoDeclarations(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

//nolint:unexported
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if len(findings) != 0 {
		t.Errorf("expected no findings for package with no declarations, got %v", findingNames(findings))
	}
}

// Test: exported field on unexported struct → no finding
func TestExportedFieldOnUnexportedStruct(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type myStruct struct {
	ExportedField string
}
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "ExportedField") {
		t.Errorf("did not expect finding for ExportedField (parent type myStruct is unexported), got %v", findingNames(findings))
	}
}

// Test: exported method on unexported type → no finding
func TestExportedMethodOnUnexportedType(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

type myStruct struct{}

func (m myStruct) ExportedMethod() {}
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{})
	if containsFinding(findings, "ExportedMethod") {
		t.Errorf("did not expect finding for ExportedMethod (receiver type myStruct is unexported), got %v", findingNames(findings))
	}
}

// Test: DedupeDetailedFindings drops findings with identical file/line/col/name.
func TestDedupeDetailedFindingsSkipsDuplicates(t *testing.T) {
	pos := token.Position{Filename: "a.go", Line: 1, Column: 1}
	f := analyzer.DetailedFinding{Finding: analyzer.Finding{Pos: pos, ObjName: "Foo", PkgName: "pkg"}}

	result := analyzer.DedupeDetailedFindings([]analyzer.DetailedFinding{f, f, f})
	if len(result) != 1 {
		t.Errorf("DedupeDetailedFindings returned %d items, want 1", len(result))
	}
}

// Test: -exclude does not suppress packages that don't match
func TestExcludeOptionNoMatch(t *testing.T) {
	pkgs := loadPackages(t, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"pkga/a.go": `package pkga

func Exported() {}
`,
		},
	}})

	findings := analyzer.Analyze(pkgs, analyzer.Options{Exclude: []string{"example.com/m/pkgb"}})
	if !containsFinding(findings, "Exported") {
		t.Errorf("expected finding for Exported (package not excluded), got %v", findingNames(findings))
	}
}

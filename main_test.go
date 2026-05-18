package main

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/packages/packagestest"
)

// Test: packages with type errors are skipped rather than aborting the run.
func TestLoadAndValidatePackagesSkipsErroredPackages(t *testing.T) {
	export := packagestest.Export(t, packagestest.Modules, []packagestest.Module{{
		Name: "example.com/m",
		Files: map[string]interface{}{
			"clean/a.go": `package clean

func Exported() {}
`,
			"broken/b.go": `package broken

var _ = UndefinedType{} // type error: UndefinedType is not defined
`,
		},
	}})
	t.Cleanup(export.Cleanup)

	cfg := export.Config
	cfg.Mode = packages.NeedName | packages.NeedFiles | packages.NeedTypes |
		packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedDeps | packages.NeedImports
	cfg.Tests = true

	pkgs, err := loadAndValidatePackages(cfg, []string{"example.com/m/clean/...", "example.com/m/broken/..."})
	if err != nil {
		t.Fatalf("expected no hard error when some packages have type errors, got: %v", err)
	}

	var cleanFound bool
	for _, pkg := range pkgs {
		if strings.HasSuffix(pkg.PkgPath, "/clean") {
			cleanFound = true
		}
		if len(pkg.Errors) > 0 {
			t.Errorf("errored package %s should have been filtered out", pkg.PkgPath)
		}
	}
	if !cleanFound {
		t.Error("clean package should be present in results even when other packages have errors")
	}
}

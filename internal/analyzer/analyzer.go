package analyzer

import (
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Finding is a symbol that is exported but never referenced outside its package.
type Finding struct {
	Pos     token.Position
	ObjName string
	PkgName string
}

// Options controls optional behavior of Analyze.
type Options struct {
	Exclude []string // package path prefixes to skip (e.g., "example.com/m/generated")
}

// Analyze runs two-pass analysis and returns findings.
func Analyze(pkgs []*packages.Package, opts Options) []Finding {
	// Filter out packages whose path matches any of the exclude prefixes.
	filtered := pkgs[:0:0]
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			filtered = append(filtered, pkg)
			continue
		}
		path := pkg.Types.Path()
		excluded := false
		for _, prefix := range opts.Exclude {
			if strings.HasPrefix(path, prefix) {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, pkg)
		}
	}
	pkgs = filtered

	exported, positions := collectExported(pkgs)
	usedExternally := markUsedExternally(pkgs, exported)
	interfaceOf := buildInterfaceOf(pkgs, exported)
	ignored, suppressedFiles := buildIgnored(pkgs)

	var findings []Finding
	for obj, pos := range positions {
		if usedExternally[obj] {
			continue
		}
		if iface, ok := interfaceOf[obj]; ok && usedExternally[iface] {
			continue
		}
		if ignored[ignoreKey{pos.Filename, pos.Line}] || ignored[ignoreKey{pos.Filename, pos.Line - 1}] {
			continue
		}
		if suppressedFiles[pos.Filename] {
			continue
		}
		findings = append(findings, Finding{
			Pos:     pos,
			ObjName: obj.Name(),
			PkgName: obj.Pkg().Name(),
		})
	}
	return findings
}

type ignoreKey struct {
	file string
	line int
}

// buildIgnored returns the set of (file, line) positions marked with
// //nolint:unexported or bare //nolint, and the set of filenames that are
// entirely suppressed by a file-level nolint comment before the first declaration.
func buildIgnored(pkgs []*packages.Package) (map[ignoreKey]bool, map[string]bool) {
	ignored := make(map[ignoreKey]bool)
	suppressedFiles := make(map[string]bool)
	seen := make(map[string]bool)

	for _, pkg := range pkgs {
		if seen[pkg.ID] || pkg.Fset == nil {
			continue
		}
		seen[pkg.ID] = true

		for _, file := range pkg.Syntax {
			// Determine file-level suppression: any nolint comment before the
			// first declaration in the file.
			var firstDeclPos token.Pos
			if len(file.Decls) > 0 {
				firstDeclPos = file.Decls[0].Pos()
			}

			filename := pkg.Fset.Position(file.Pos()).Filename

			for _, cg := range file.Comments {
				for _, c := range cg.List {
					if isIgnoreComment(c.Text) {
						pos := pkg.Fset.Position(c.Pos())
						ignored[ignoreKey{pos.Filename, pos.Line}] = true

						// File-level suppression: comment before the first declaration.
						if firstDeclPos != token.NoPos && c.Pos() < firstDeclPos {
							suppressedFiles[filename] = true
						} else if firstDeclPos == token.NoPos {
							// No declarations at all — suppress the file anyway.
							suppressedFiles[filename] = true
						}
					}
				}
			}
		}
	}
	return ignored, suppressedFiles
}

func isIgnoreComment(text string) bool {
	text = strings.TrimSpace(strings.TrimPrefix(text, "//"))
	if text == "nolint" {
		return true
	}
	if strings.HasPrefix(text, "nolint:") {
		for _, name := range strings.Split(strings.TrimPrefix(text, "nolint:"), ",") {
			if strings.TrimSpace(name) == "unexported" {
				return true
			}
		}
	}
	return false
}

// collectExported returns all exported objects and their declaration positions.
func collectExported(pkgs []*packages.Package) (map[types.Object]bool, map[types.Object]token.Position) {
	exported := make(map[types.Object]bool)
	positions := make(map[types.Object]token.Position)
	seen := make(map[string]bool)

	for _, pkg := range pkgs {
		if seen[pkg.ID] || pkg.TypesInfo == nil {
			continue
		}
		seen[pkg.ID] = true

		for ident, obj := range pkg.TypesInfo.Defs {
			if obj == nil || !obj.Exported() || obj.Pkg() == nil {
				continue
			}
			switch obj.(type) {
			case *types.Func, *types.TypeName, *types.Var, *types.Const:
				exported[obj] = true
				positions[obj] = pkg.Fset.Position(ident.Pos())
			}
		}
	}
	return exported, positions
}

// markUsedExternally returns objects referenced from a package other than their own.
func markUsedExternally(pkgs []*packages.Package, exported map[types.Object]bool) map[types.Object]bool {
	usedExternally := make(map[types.Object]bool)
	seen := make(map[string]bool)

	for _, pkg := range pkgs {
		if seen[pkg.ID] || pkg.TypesInfo == nil {
			continue
		}
		seen[pkg.ID] = true

		for _, obj := range pkg.TypesInfo.Uses {
			if obj == nil || obj.Pkg() == nil || !exported[obj] {
				continue
			}
			if obj.Pkg().Path() != pkg.Types.Path() {
				usedExternally[obj] = true
			}
		}

		for _, sel := range pkg.TypesInfo.Selections {
			obj := sel.Obj()
			if obj == nil || obj.Pkg() == nil || !exported[obj] {
				continue
			}
			if obj.Pkg().Path() != pkg.Types.Path() {
				usedExternally[obj] = true
			}
		}
	}
	return usedExternally
}

// buildInterfaceOf maps each exported method of an exported interface to that interface's TypeName.
func buildInterfaceOf(pkgs []*packages.Package, exported map[types.Object]bool) map[types.Object]types.Object {
	interfaceOf := make(map[types.Object]types.Object)
	seen := make(map[string]bool)

	for _, pkg := range pkgs {
		if seen[pkg.ID] || pkg.TypesInfo == nil {
			continue
		}
		seen[pkg.ID] = true

		for _, obj := range pkg.TypesInfo.Defs {
			tn, ok := obj.(*types.TypeName)
			if !ok || !exported[tn] {
				continue
			}
			iface, ok := tn.Type().Underlying().(*types.Interface)
			if !ok {
				continue
			}
			for i := 0; i < iface.NumMethods(); i++ {
				m := iface.Method(i)
				interfaceOf[m] = tn // no exported[m] check
			}
		}
	}
	return interfaceOf
}


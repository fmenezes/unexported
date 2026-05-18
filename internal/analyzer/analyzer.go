package analyzer

import (
	"fmt"
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

// DetailedFinding contains finding metadata plus the concrete object.
// Obj is intentionally exported so callers that implement fixes can rename
// all in-package references safely via go/types identities.
type DetailedFinding struct {
	Finding
	Obj types.Object
}

// Options controls optional behavior of Analyze.
type Options struct {
	Exclude []string // package path prefixes to skip (e.g., "example.com/m/generated")
}

// Analyze runs two-pass analysis and returns findings.
func Analyze(pkgs []*packages.Package, opts Options) []Finding {
	detailed := AnalyzeDetailed(pkgs, opts)
	findings := make([]Finding, 0, len(detailed))
	for _, f := range detailed {
		findings = append(findings, f.Finding)
	}
	return findings
}

// DedupeDetailedFindings removes duplicate findings that share the same file, line, column, and symbol name.
// Duplicates arise when test variants of a package produce identical positions.
func DedupeDetailedFindings(findings []DetailedFinding) []DetailedFinding {
	seen := make(map[string]bool)
	deduped := make([]DetailedFinding, 0, len(findings))
	for _, f := range findings {
		key := fmt.Sprintf("%s:%d:%d:%s", f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.ObjName)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, f)
	}
	return deduped
}

// AnalyzeDetailed runs two-pass analysis and returns findings with object identities.
func AnalyzeDetailed(pkgs []*packages.Package, opts Options) []DetailedFinding {
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

	var findings []DetailedFinding
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
		findings = append(findings, DetailedFinding{
			Finding: Finding{
				Pos:     pos,
				ObjName: obj.Name(),
				PkgName: obj.Pkg().Name(),
			},
			Obj: obj,
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

		fieldParentExported := buildFieldParentExported(pkg)

		for ident, obj := range pkg.TypesInfo.Defs {
			if obj == nil || !obj.Exported() || obj.Pkg() == nil {
				continue
			}
			switch v := obj.(type) {
			case *types.Func:
				if !receiverTypeExported(v) {
					continue
				}
				exported[obj] = true
				positions[obj] = pkg.Fset.Position(ident.Pos())
			case *types.Var:
				if parentExported, isField := fieldParentExported[v]; isField && !parentExported {
					continue
				}
				exported[obj] = true
				positions[obj] = pkg.Fset.Position(ident.Pos())
			case *types.TypeName, *types.Const:
				exported[obj] = true
				positions[obj] = pkg.Fset.Position(ident.Pos())
			}
		}
	}
	return exported, positions
}

// buildFieldParentExported maps each struct field to whether its containing struct type is exported.
func buildFieldParentExported(pkg *packages.Package) map[*types.Var]bool {
	result := make(map[*types.Var]bool)
	for _, obj := range pkg.TypesInfo.Defs {
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		st, ok := tn.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < st.NumFields(); i++ {
			result[st.Field(i)] = tn.Exported()
		}
	}
	return result
}

// receiverTypeExported returns false for methods whose receiver type is unexported.
// Package-level functions (no receiver) always return true.
func receiverTypeExported(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return true
	}
	recv := sig.Recv()
	if recv == nil {
		return true
	}
	recvType := recv.Type()
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	return !ok || named.Obj().Exported()
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

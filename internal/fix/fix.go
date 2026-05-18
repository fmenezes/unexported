package fix

import (
	"fmt"
	"go/types"
	"os"
	"sort"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"

	"github.com/fmenezes/unexported/internal/analyzer"
)

type textEdit struct {
	start int
	end   int
	old   string
	new   string
}

// ApplyFixes renames each reported exported symbol to its unexported equivalent.
// Returns the number of renames applied and skipped (due to naming conflicts).
func ApplyFixes(pkgs []*packages.Package, findings []analyzer.DetailedFinding) (applied int, skipped int, err error) {
	targets := make(map[types.Object]string)
	for _, f := range findings {
		if f.Obj == nil {
			continue
		}
		newName := unexportName(f.ObjName)
		if newName == f.ObjName {
			continue
		}
		if hasConflict(f.Obj, newName) {
			skipped++
			continue
		}
		targets[f.Obj] = newName
	}

	if len(targets) == 0 {
		return 0, skipped, nil
	}

	seen := make(map[string]bool)
	editsByFile := make(map[string][]textEdit)
	seenPkgs := make(map[string]bool)
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil || pkg.Fset == nil || seenPkgs[pkg.ID] {
			continue
		}
		seenPkgs[pkg.ID] = true

		for ident, obj := range pkg.TypesInfo.Defs {
			newName, ok := targets[obj]
			if !ok {
				continue
			}
			pos := pkg.Fset.PositionFor(ident.Pos(), false)
			if !pos.IsValid() {
				continue
			}
			key := fmt.Sprintf("%s:%d:%d:%s", pos.Filename, pos.Offset, len(ident.Name), newName)
			if seen[key] {
				continue
			}
			seen[key] = true
			editsByFile[pos.Filename] = append(editsByFile[pos.Filename], textEdit{
				start: pos.Offset,
				end:   pos.Offset + len(ident.Name),
				old:   ident.Name,
				new:   newName,
			})
		}

		for ident, obj := range pkg.TypesInfo.Uses {
			newName, ok := targets[obj]
			if !ok {
				continue
			}
			pos := pkg.Fset.PositionFor(ident.Pos(), false)
			if !pos.IsValid() {
				continue
			}
			key := fmt.Sprintf("%s:%d:%d:%s", pos.Filename, pos.Offset, len(ident.Name), newName)
			if seen[key] {
				continue
			}
			seen[key] = true
			editsByFile[pos.Filename] = append(editsByFile[pos.Filename], textEdit{
				start: pos.Offset,
				end:   pos.Offset + len(ident.Name),
				old:   ident.Name,
				new:   newName,
			})
		}
	}

	for filename, edits := range editsByFile {
		if len(edits) == 0 {
			continue
		}
		content, readErr := os.ReadFile(filename)
		if readErr != nil {
			return 0, skipped, fmt.Errorf("read %s: %w", filename, readErr)
		}

		sort.Slice(edits, func(i, j int) bool {
			return edits[i].start > edits[j].start
		})

		for _, edit := range edits {
			if edit.start < 0 || edit.end > len(content) || edit.start >= edit.end {
				return 0, skipped, fmt.Errorf("invalid edit range for %s", filename)
			}
			if string(content[edit.start:edit.end]) != edit.old {
				return 0, skipped, fmt.Errorf("text mismatch at %s:%d", filename, edit.start)
			}
			content = append(content[:edit.start], append([]byte(edit.new), content[edit.end:]...)...)
		}

		if writeErr := os.WriteFile(filename, content, 0o644); writeErr != nil {
			return 0, skipped, fmt.Errorf("write %s: %w", filename, writeErr)
		}
	}

	return len(targets), skipped, nil
}

func hasConflict(obj types.Object, newName string) bool {
	if obj == nil || obj.Parent() == nil {
		return false
	}
	existing := obj.Parent().Lookup(newName)
	return existing != nil && existing != obj
}

func unexportName(name string) string {
	r, size := utf8.DecodeRuneInString(name)
	if size == 0 || !unicode.IsUpper(r) {
		return name
	}
	return string(unicode.ToLower(r)) + name[size:]
}

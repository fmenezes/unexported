# unexported

A Go linter that warns about exported symbols that are never referenced outside their declaring package.

Unlike `staticcheck`'s `unused` (U1000), which deliberately skips exported symbols, `unexported` performs whole-codebase analysis to find accidentally exported declarations — functions, methods, types, variables, constants, and struct fields that could safely be made unexported.

## Install

```bash
go install github.com/fmenezes/unexported@latest
```

## Usage

```bash
unexported ./...
unexported ./internal/...
unexported -tags e2e ./...
unexported -json ./... | jq .
```

## Flags

| Flag | Description |
|------|-------------|
| `-fix` | Rename reported exported symbols to their unexported equivalents in place |
| `-tags` | Comma-separated build tags (e.g. `e2e,integration`) |
| `-json` | Emit findings as JSON objects, one per line |
| `-exclude` | Comma-separated package path prefixes to skip (e.g. `example.com/m/generated`) |
| `-max` | Maximum number of findings to show; `0` means unlimited (default `0`) |
| `-version` | Print version and exit |

## Output

```
internal/store/foo.go:42:1: Foo is exported but never referenced outside package store, consider unexporting
```

With `-json`:
```json
{"col":1,"file":"internal/store/foo.go","line":42,"message":"Foo is exported but never referenced outside package store, consider unexporting"}
```

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | No findings |
| `1` | One or more findings |
| `2` | Tool error (bad pattern, build failure) |

## Suppressing findings

Add `//nolint:unexported` or bare `//nolint` on the declaration line, on the line above it, or at the top of a file (before the first declaration) to suppress all findings in that file:

```go
//nolint:unexported  ← suppresses everything in this file
package mypackage

func Exported() {} //nolint:unexported  ← suppresses this declaration only

//nolint
func AlsoSuppressed() {}
```

## How it works

Two passes over the fully type-checked package graph:

1. **Collect** — walks `types.Info.Defs` across all packages to record every exported symbol and its declaration position.
2. **Mark** — walks `types.Info.Uses` and `types.Info.Selections` across all packages to mark symbols referenced from a package other than their own.

Symbols that survive both passes without being marked are reported. Test files in other packages count as external usage. Methods of an exported interface are suppressed when the interface itself is used externally. Packages matching any `-exclude` prefix are skipped entirely before both passes.

## When this is useful

This tool is designed for self-contained codebases and modules where you can verify that no external consumer exists. It is particularly effective for `internal/` packages, where Go already prevents external imports — making exportedness purely a style choice within the module.

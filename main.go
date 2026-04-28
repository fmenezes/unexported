package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/fmenezes/unexported/internal/analyzer"
)

var version = "dev"

func main() {
	jsonOutput := flag.Bool("json", false, "emit findings as JSON objects, one per line")
	tags := flag.String("tags", "", "comma-separated list of build tags (e.g. e2e,integration)")
	excludeFlag := flag.String("exclude", "", "comma-separated package path prefixes to skip")
	maxFlag := flag.Int("max", 0, "maximum number of findings to show (0 = unlimited)")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: unexported [flags] [packages]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  unexported ./...\n")
		fmt.Fprintf(os.Stderr, "  unexported -json ./internal/...\n")
	}
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		os.Exit(0)
	}

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles |
			packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedDeps | packages.NeedImports,
		Tests: true,
	}
	if *tags != "" {
		cfg.BuildFlags = []string{"-tags=" + *tags}
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unexported: loading packages: %v\n", err)
		os.Exit(2)
	}

	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			fmt.Fprintf(os.Stderr, "unexported: %v\n", e)
		}
		if len(pkg.Errors) > 0 {
			os.Exit(2)
		}
	}

	var excludeList []string
	if *excludeFlag != "" {
		excludeList = strings.Split(*excludeFlag, ",")
	}

	findings := analyzer.Analyze(pkgs, analyzer.Options{Exclude: excludeList})
	if len(findings) == 0 {
		os.Exit(0)
	}

	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i].Pos, findings[j].Pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})

	// Deduplicate: test variants of packages produce duplicate findings at the same position.
	seen := make(map[string]bool)
	deduped := findings[:0]
	for _, f := range findings {
		key := fmt.Sprintf("%s:%d:%d:%s", f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.ObjName)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, f)
		}
	}
	findings = deduped

	if *maxFlag > 0 && len(findings) > *maxFlag {
		fmt.Fprintf(os.Stderr, "unexported: showing %d of %d findings (use -max=0 for all)\n",
			*maxFlag, len(findings))
		findings = findings[:*maxFlag]
	}

	for _, f := range findings {
		msg := fmt.Sprintf("%s is exported but never referenced outside package %s, consider unexporting",
			f.ObjName, f.PkgName)
		if *jsonOutput {
			b, _ := json.Marshal(map[string]any{
				"file":    f.Pos.Filename,
				"line":    f.Pos.Line,
				"col":     f.Pos.Column,
				"message": msg,
			})
			fmt.Println(string(b))
		} else {
			fmt.Printf("%s:%d:%d: %s\n", f.Pos.Filename, f.Pos.Line, f.Pos.Column, msg)
		}
	}

	os.Exit(1)
}

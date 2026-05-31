// godoc-coverage walks the Go packages or individual files matching the
// supplied patterns and reports the fraction of exported identifiers
// (functions, methods, types, vars, consts) that carry a doc comment.
// Exits with a non-zero status if the overall coverage is below
// --threshold.
//
// Two input shapes are accepted in the positional args:
//
//   - Package patterns: "./pkg" (single directory) or "./pkg/..."
//     (recursive). Every non-test .go file in the matched directory
//     contributes to the coverage stats.
//   - Individual files: any positional arg ending in ".go". Only that
//     file's exports count, even if it lives in a package whose other
//     files would otherwise be in scope. Useful while a package is
//     being documented incrementally and the gate should only cover the
//     subset that has been brought up to the threshold so far.
//
// The two shapes can be mixed in a single invocation.
//
// Test files (_test.go) are ignored: documenting test fixtures adds
// noise without value.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// stats tracks the documented vs total exported identifier counts for a
// single package or for the aggregate run.
type stats struct {
	total      int
	documented int
}

// pct returns the documented / total ratio expressed as a percentage,
// or 100 when total is zero (a package with no exported surface trivially
// satisfies any threshold).
func (s stats) pct() float64 {
	if s.total == 0 {
		return 100
	}
	return float64(s.documented) / float64(s.total) * 100
}

// missingEntry is a single undocumented exported identifier, used in the
// final report so a contributor knows which symbols to fix.
type missingEntry struct {
	pkg  string
	file string
	line int
	name string
	kind string
}

func main() {
	threshold := flag.Float64("threshold", 80, "minimum acceptable coverage percentage")
	verbose := flag.Bool("verbose", false, "list every undocumented exported identifier")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		fmt.Fprintln(os.Stderr, "usage: godoc-coverage [--threshold=N] [--verbose] <pkg-path>...")
		os.Exit(2)
	}

	scope, err := expandPatterns(patterns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "expanding patterns: %v\n", err)
		os.Exit(2)
	}

	var overall stats
	var missing []missingEntry
	perPkg := make(map[string]stats)

	for dir, fileFilter := range scope.dirs {
		ps, m, err := analysePackageDir(dir, fileFilter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "analysing %s: %v\n", dir, err)
			os.Exit(2)
		}
		for _, s := range ps {
			perPkg[dir] = s
			overall.total += s.total
			overall.documented += s.documented
		}
		missing = append(missing, m...)
	}

	names := make([]string, 0, len(perPkg))
	for n := range perPkg {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Println("godoc coverage by package:")
	for _, n := range names {
		s := perPkg[n]
		fmt.Printf("  %-60s %3d/%-3d  %6.2f%%\n", n, s.documented, s.total, s.pct())
	}
	fmt.Printf("\noverall: %d/%d  %.2f%%  (threshold %.2f%%)\n",
		overall.documented, overall.total, overall.pct(), *threshold)

	if *verbose || overall.pct() < *threshold {
		if len(missing) > 0 {
			fmt.Println("\nundocumented exported identifiers:")
			sort.SliceStable(missing, func(i, j int) bool {
				if missing[i].file != missing[j].file {
					return missing[i].file < missing[j].file
				}
				return missing[i].line < missing[j].line
			})
			for _, m := range missing {
				fmt.Printf("  %s:%d  %s %s\n", m.file, m.line, m.kind, m.name)
			}
		}
	}

	if overall.pct() < *threshold {
		fmt.Fprintf(os.Stderr,
			"\ndocstring coverage %.2f%% is below threshold %.2f%%\n",
			overall.pct(), *threshold)
		os.Exit(1)
	}
}

// scopeSet captures the analyse-this set after pattern expansion. dirs
// maps each package directory we will scan to an optional file
// allowlist; a nil allowlist for a directory means "every .go file in
// that dir", while a non-nil allowlist means "only these specific
// basenames within that dir". File-path positional args populate the
// allowlist form; package patterns ("./pkg" / "./pkg/...") populate the
// all-files form.
type scopeSet struct {
	dirs map[string]map[string]bool
}

// addDir registers a package directory in the all-files form.
func (s *scopeSet) addDir(dir string) {
	if _, ok := s.dirs[dir]; ok {
		return
	}
	s.dirs[dir] = nil
}

// addFile registers a single .go file as the only file of interest in
// its parent directory. If the directory was previously registered in
// the all-files form, the file allowlist is left unset so the directory
// continues to be analysed in full (a more permissive scope always
// wins).
func (s *scopeSet) addFile(file string) {
	dir := filepath.Dir(file)
	base := filepath.Base(file)
	existing, present := s.dirs[dir]
	if present && existing == nil {
		return
	}
	if existing == nil {
		existing = map[string]bool{}
	}
	existing[base] = true
	s.dirs[dir] = existing
}

// expandPatterns resolves the user-supplied positional args into a
// scopeSet. Supported shapes:
//
//   - "./path/..." or "path/...": recursive directory walk; every
//     package directory under root with at least one non-test .go file
//     is registered in the all-files form.
//   - "./path" or "path" (a directory): single package, all-files form.
//   - Any arg ending in ".go" that refers to an existing file: file
//     allowlist form for that file's parent directory.
//
// Filesystem paths are evaluated relative to the working directory.
func expandPatterns(patterns []string) (*scopeSet, error) {
	scope := &scopeSet{dirs: make(map[string]map[string]bool)}
	for _, p := range patterns {
		if strings.HasSuffix(p, ".go") {
			info, err := os.Stat(p)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if info.IsDir() {
				return nil, fmt.Errorf("%s: looks like a Go file but is a directory", p)
			}
			scope.addFile(filepath.Clean(p))
			continue
		}
		recursive := strings.HasSuffix(p, "/...")
		root := strings.TrimSuffix(p, "/...")
		root = strings.TrimPrefix(root, "./")
		if root == "" {
			root = "."
		}
		if !recursive {
			if _, err := os.Stat(root); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			scope.addDir(root)
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			base := filepath.Base(path)
			if base == "vendor" || base == "testdata" || strings.HasPrefix(base, ".") && path != root {
				return filepath.SkipDir
			}
			entries, readErr := os.ReadDir(path)
			if readErr != nil {
				return readErr
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
					scope.addDir(path)
					return nil
				}
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("%s: %w", p, walkErr)
		}
	}
	return scope, nil
}

// analysePackageDir parses every non-test .go file in dir and returns
// per-package stats plus the list of undocumented exported identifiers.
// When fileFilter is non-nil, only identifiers declared in basenames
// present in the map contribute to the stats; nil filter means count
// everything.
//
// A single directory may host more than one package (rare: typically
// `package foo` and `package foo_test`), so the result is a map.
func analysePackageDir(dir string, fileFilter map[string]bool) (map[string]stats, []missingEntry, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}
	result := make(map[string]stats)
	var missing []missingEntry
	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		dpkg := doc.New(pkg, "./"+dir, doc.AllDecls)
		s, m := walkDocPackage(dpkg, fset, fileFilter)
		result[dir] = s
		missing = append(missing, m...)
	}
	return result, missing, nil
}

// walkDocPackage counts documented vs undocumented exported identifiers
// across the doc.Package. Method receivers count individually so an
// interface implementation with seven undocumented methods registers
// as seven missing entries, not one. When fileFilter is non-nil only
// identifiers declared in matching basenames are recorded.
func walkDocPackage(p *doc.Package, fset *token.FileSet, fileFilter map[string]bool) (stats, []missingEntry) {
	var s stats
	var missing []missingEntry

	record := func(name, kind string, hasDoc bool, pos token.Pos) {
		if !ast.IsExported(name) {
			return
		}
		fp := fset.Position(pos)
		if fileFilter != nil && !fileFilter[filepath.Base(fp.Filename)] {
			return
		}
		s.total++
		if hasDoc {
			s.documented++
			return
		}
		missing = append(missing, missingEntry{
			pkg:  p.Name,
			file: fp.Filename,
			line: fp.Line,
			name: name,
			kind: kind,
		})
	}

	for _, f := range p.Funcs {
		record(f.Name, "func", strings.TrimSpace(f.Doc) != "", f.Decl.Pos())
	}
	for _, t := range p.Types {
		record(t.Name, "type", strings.TrimSpace(t.Doc) != "", t.Decl.Pos())
		for _, m := range t.Methods {
			record(m.Name, "method", strings.TrimSpace(m.Doc) != "", m.Decl.Pos())
		}
		for _, fn := range t.Funcs {
			record(fn.Name, "func", strings.TrimSpace(fn.Doc) != "", fn.Decl.Pos())
		}
		for _, v := range t.Vars {
			for _, n := range v.Names {
				record(n, "var", strings.TrimSpace(v.Doc) != "", v.Decl.Pos())
			}
		}
		for _, c := range t.Consts {
			for _, n := range c.Names {
				record(n, "const", strings.TrimSpace(c.Doc) != "", c.Decl.Pos())
			}
		}
	}
	for _, v := range p.Vars {
		for _, n := range v.Names {
			record(n, "var", strings.TrimSpace(v.Doc) != "", v.Decl.Pos())
		}
	}
	for _, c := range p.Consts {
		for _, n := range c.Names {
			record(n, "const", strings.TrimSpace(c.Doc) != "", c.Decl.Pos())
		}
	}
	return s, missing
}

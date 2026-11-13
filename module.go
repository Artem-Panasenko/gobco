package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Module mode (-module): instrument several packages of the module in the
// current directory without copying it. The instrumented files go to the
// temporary directory, together with an overlay.json for "go test -overlay",
// so one ordinary "go test" run over the module collects the counters of
// every instrumented package.

const regSource = `// Package gobcoreg lets a test binary persist the counters of every
// instrumented package that is linked into it.
package gobcoreg

import "sync"

var (
	mu  sync.Mutex
	fns []func()
)

func Register(f func()) {
	mu.Lock()
	fns = append(fns, f)
	mu.Unlock()
}

func Flush() {
	mu.Lock()
	defer mu.Unlock()
	for _, f := range fns {
		f()
	}
}
`

// modulePkg is a package of the module that is instrumented.
type modulePkg struct {
	// Relative to the module root, using '/' as separator.
	dir string

	// Whether to leave the package out of the report.
	excluded bool

	// The conditions of the package, in the order of their indexes
	// in the instrumented code.
	conds []condition
}

// runModule covers the packages given on the command line
// by running the tests of the whole module once.
func (g *gobco) runModule() {
	modRoot, modPath := g.findModule()
	pkgs := g.listPackages(modRoot, g.patterns)
	if len(g.exclude) > 0 {
		excluded := map[string]bool{}
		for _, pkg := range g.listPackages(modRoot, g.exclude) {
			excluded[pkg.dir] = true
		}
		if len(excluded) == 0 {
			g.check(fmt.Errorf("gobco: -exclude %s matches no package",
				strings.Join(g.exclude, " ")))
		}
		for i := range pkgs {
			pkgs[i].excluded = excluded[pkgs[i].dir]
		}
	}

	// From here on, all paths are relative to the module root,
	// including those in the report.
	g.check(os.Chdir(modRoot))

	overlayFile := g.instrumentModule(modRoot, modPath+"/gobcoreg", pkgs)
	if overlayFile == "" {
		g.outf("nothing to instrument")
		return
	}

	statsDir := g.file("stats")
	g.check(os.MkdirAll(statsDir, 0o777))
	g.runModuleTests(overlayFile, statsDir)
	g.mergeStats(statsDir, pkgs)

	var reports []pkgReport
	for _, pkg := range pkgs {
		if !pkg.excluded {
			reports = append(reports, pkgReport{pkg.dir, pkg.conds})
		}
	}
	g.report(reports)
}

// findModule returns the root directory and the path of the module
// in the current directory.
func (g *gobco) findModule() (root, path string) {
	mod := g.goList("-m", "-f", "{{.Dir}}\t{{.Path}}")
	if strings.Contains(mod, "\n") {
		g.check(errors.New("gobco: -module needs exactly one module"))
	}
	fields := strings.SplitN(mod, "\t", 2)
	root, path = fields[0], fields[1]
	if _, err := os.Stat(filepath.Join(root, "gobcoreg")); err == nil {
		g.check(errors.New("gobco: directory gobcoreg already exists"))
	}
	return
}

// listPackages returns the packages of the module that match the patterns.
//
// A pattern that starts with a directory, such as "internal/...", means
// that directory, not an import path from the standard library.
func (g *gobco) listPackages(modRoot string, patterns []string) []modulePkg {
	args := []string{"-find", "-e", "-f", "{{.Dir}}"}
	for _, pattern := range patterns {
		first := strings.SplitN(filepath.ToSlash(pattern), "/", 2)[0]
		st, err := os.Stat(first)
		if err == nil && st.IsDir() && !strings.HasPrefix(pattern, ".") && !filepath.IsAbs(pattern) {
			pattern = "./" + pattern
		}
		args = append(args, pattern)
	}

	var pkgs []modulePkg
	for _, dir := range strings.Split(g.goList(args...), "\n") {
		if dir == "" {
			continue // a package that "go list -e" could not locate
		}
		rel, err := filepath.Rel(modRoot, dir)
		g.check(err)
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			g.check(fmt.Errorf("gobco: package %s is outside the module", dir))
		}
		pkgs = append(pkgs, modulePkg{dir: filepath.ToSlash(rel)})
	}
	return pkgs
}

// exportImporter returns an importer that reads the types of the
// dependencies from the build cache, instead of type-checking their source
// again for each package.
func (g *gobco) exportImporter() types.Importer {
	exportFiles := map[string]string{}
	exports := g.goList("-e", "-export", "-deps", "-test",
		"-f", "{{if .Export}}{{.ImportPath}}\t{{.Export}}{{end}}", "./...")
	for _, line := range strings.Split(exports, "\n") {
		fields := strings.SplitN(line, "\t", 2)
		// Skip the variants recompiled for a test, such as "p [p.test]".
		if len(fields) == 2 && !strings.Contains(fields[0], " ") {
			exportFiles[fields[0]] = fields[1]
		}
	}

	lookup := func(path string) (io.ReadCloser, error) {
		file, found := exportFiles[path]
		if !found {
			return nil, fmt.Errorf("gobco: no export data for %q", path)
		}
		return os.Open(file)
	}
	return importer.ForCompiler(token.NewFileSet(), "gc", lookup)
}

// instrumentModule writes the instrumented packages to the temporary
// directory and returns the overlay file that makes "go test" use them,
// or "" if there is nothing to instrument.
//
// The stats files of a package are named after its index in pkgs.
func (g *gobco) instrumentModule(modRoot, regPath string, pkgs []modulePkg) string {
	imp := g.exportImporter()
	overlay := map[string]string{}
	for idx := range pkgs {
		pkg := &pkgs[idx]
		dst := g.file(filepath.Join("overlay", strconv.Itoa(idx)))
		g.check(os.MkdirAll(dst, 0o777))

		in := g.newInstrumenter()
		in.importer = imp
		in.regPath = regPath
		in.statsName = strconv.Itoa(idx)
		if !in.instrument(filepath.FromSlash(pkg.dir), "", dst) {
			continue
		}
		g.verbosef("Instrumented %s to %s", pkg.dir, dst)

		entries, err := os.ReadDir(dst)
		g.check(err)
		for _, entry := range entries {
			src := filepath.Join(modRoot, filepath.FromSlash(pkg.dir), entry.Name())
			overlay[src] = filepath.Join(dst, entry.Name())
		}

		pkg.conds = make([]condition, len(in.conds))
		for i, c := range in.conds {
			pkg.conds[i] = condition{Start: c.pos, Code: c.text}
		}
	}
	if len(overlay) == 0 {
		return ""
	}

	reg := g.file("gobcoreg.go")
	writeFile(reg, regSource)
	overlay[filepath.Join(modRoot, "gobcoreg", "gobcoreg.go")] = reg

	data, err := json.MarshalIndent(map[string]interface{}{"Replace": overlay}, "", "\t")
	g.check(err)
	overlayFile := g.file("overlay.json")
	writeFile(overlayFile, string(data))
	return overlayFile
}

// runModuleTests runs the tests of the whole module on the instrumented
// code. Each test process saves its counters to statsDir.
func (g *gobco) runModuleTests(overlayFile, statsDir string) {
	// "go vet" cannot handle the registry package,
	// which only exists in the overlay.
	extraArgs := append([]string{"-overlay", overlayFile, "-vet=off"}, g.goTestArgs...)
	args := goTest{}.args(g.verbose, "./...", extraArgs)
	cmd := exec.Command("go", args[1:]...)
	cmd.Stdout = g.stdout
	cmd.Stderr = g.stderr
	cmd.Env = append(os.Environ(), "GOBCO_STATS_DIR="+statsDir)

	g.verbosef("Running %q", strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		g.errf("go test ./...: %s", err)
		g.exitCode = 1
	}
}

// mergeStats adds the counters from the stats files of all test processes
// to the conditions of the packages.
func (g *gobco) mergeStats(statsDir string, pkgs []modulePkg) {
	files, err := filepath.Glob(filepath.Join(statsDir, "*.json"))
	g.check(err)
	for _, file := range files {
		// See filename in templates/gobco_module.go.
		idx, err := strconv.Atoi(strings.SplitN(filepath.Base(file), ".", 2)[0])
		if err != nil || idx < 0 || idx >= len(pkgs) {
			g.check(fmt.Errorf("gobco: unexpected stats file %s", file))
		}
		conds := pkgs[idx].conds

		counts, err := g.load(file)
		g.check(err)
		if len(counts) != len(conds) {
			g.check(fmt.Errorf("gobco: stats file %s has %d conditions instead of %d",
				file, len(counts), len(conds)))
		}
		for i, c := range counts {
			conds[i].TrueCount += c.TrueCount
			conds[i].FalseCount += c.FalseCount
		}
	}
}

// goList runs "go list" in the current directory and returns its output.
func (g *gobco) goList(args ...string) string {
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Stderr = g.stderr
	out, err := cmd.Output()
	g.check(err)
	return strings.TrimSpace(string(out))
}

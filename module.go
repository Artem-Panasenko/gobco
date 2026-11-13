package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
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
	importPath string

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
	g.modRoot, g.regPath = modRoot, modPath+"/gobcoreg"
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

	// The cover tool reads the source files from the disk and ignores
	// the overlay, so a statement coverage profile needs a copy of the module.
	tree := ""
	if g.coverProfile != "" {
		tree = g.mirrorModule(modRoot)
	}

	overlayFile, found := g.instrumentModule(modRoot, g.regPath, tree, pkgs)
	if !found {
		g.outf("nothing to instrument")
		return
	}

	statsDir := g.file("stats")
	g.check(os.MkdirAll(statsDir, 0o777))
	if !g.runModuleTests(overlayFile, statsDir, tree) {
		return
	}
	g.mergeStats(statsDir, pkgs)
	if tree != "" {
		g.statements = g.writeCoverProfile(g.file("cover.out"), g.coverProfile)
	}

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
	args := []string{"-find", "-e", "-f", "{{.ImportPath}}\t{{.Dir}}"}
	for _, pattern := range patterns {
		first := strings.SplitN(filepath.ToSlash(pattern), "/", 2)[0]
		st, err := os.Stat(first)
		if err == nil && st.IsDir() && !strings.HasPrefix(pattern, ".") && !filepath.IsAbs(pattern) {
			pattern = "./" + pattern
		}
		args = append(args, pattern)
	}

	var pkgs []modulePkg
	for _, line := range strings.Split(g.goList(args...), "\n") {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || fields[1] == "" {
			continue // a package that "go list -e" could not locate
		}
		importPath, dir := fields[0], fields[1]
		rel, err := filepath.Rel(modRoot, dir)
		g.check(err)
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			g.check(fmt.Errorf("gobco: package %s is outside the module", dir))
		}
		pkgs = append(pkgs, modulePkg{importPath: importPath, dir: filepath.ToSlash(rel)})
	}
	return pkgs
}

// exportImporter returns an importer that reads the types of the
// dependencies from the build cache, instead of type-checking their source
// again for each package.
func (g *gobco) exportImporter() types.Importer {
	exportFiles := map[string]string{}
	// Without "-e", a package that does not compile stops the run
	// with the message from the compiler.
	exports := g.goList("-export", "-deps", "-test",
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

// instrumentModule writes the instrumented packages and returns whether
// there was anything to instrument.
//
// Without a copy of the module (tree), the instrumented files go to the
// temporary directory, and the result is the overlay file that makes
// "go test" use them. Otherwise they replace the files in the copy.
//
// The stats files of a package are named after its index in pkgs.
func (g *gobco) instrumentModule(modRoot, regPath, tree string, pkgs []modulePkg) (overlayFile string, found bool) {
	imp := g.exportImporter()
	overlay := map[string]string{}
	for idx := range pkgs {
		pkg := &pkgs[idx]
		dst := g.file(filepath.Join("overlay", strconv.Itoa(idx)))
		if tree != "" {
			dst = filepath.Join(tree, filepath.FromSlash(pkg.dir))
			g.removeGoFiles(dst)
		}
		g.check(os.MkdirAll(dst, 0o777))

		in := g.newInstrumenter()
		in.importer = imp
		in.regPath = regPath
		in.statsName = strconv.Itoa(idx)
		in.importPath = pkg.importPath
		if !in.instrument(filepath.FromSlash(pkg.dir), "", dst) {
			continue
		}
		g.verbosef("Instrumented %s to %s", pkg.dir, dst)
		found = true

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
	if !found {
		return "", false
	}

	if tree != "" {
		g.check(os.MkdirAll(filepath.Join(tree, "gobcoreg"), 0o777))
		writeFile(filepath.Join(tree, "gobcoreg", "gobcoreg.go"), regSource)
		return "", true
	}

	reg := g.file("gobcoreg.go")
	writeFile(reg, regSource)
	overlay[filepath.Join(modRoot, "gobcoreg", "gobcoreg.go")] = reg

	data, err := json.MarshalIndent(map[string]interface{}{"Replace": overlay}, "", "\t")
	g.check(err)
	overlayFile = g.file("overlay.json")
	writeFile(overlayFile, string(data))
	return overlayFile, true
}

// removeGoFiles removes the Go files from the directory,
// since the instrumenter writes all of them again.
func (g *gobco) removeGoFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	g.check(err)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") {
			g.check(os.Remove(filepath.Join(dir, entry.Name())))
		}
	}
}

// mirrorModule copies the module to a directory that depends only on the
// module, so that the build cache can be used by the next run.
// The vendor directory is linked instead of copied.
func (g *gobco) mirrorModule(modRoot string) string {
	sum := sha256.Sum256([]byte(modRoot))
	tree := filepath.Join(os.TempDir(), "gobco-tree-"+hex.EncodeToString(sum[:4]))
	g.treeDir = tree
	g.check(os.RemoveAll(tree))
	g.check(os.MkdirAll(tree, 0o777))

	g.check(filepath.WalkDir(modRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(modRoot, path)
		if err != nil || rel == "." {
			return err
		}
		dst := filepath.Join(tree, rel)
		switch {
		case d.Name() == ".git" && d.IsDir():
			return filepath.SkipDir
		case rel == "vendor":
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir():
			return os.MkdirAll(dst, 0o777)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			if err := copyFile(path, dst); err != nil {
				return err
			}
			return os.Chmod(dst, info.Mode().Perm())
		}
		return nil
	}))
	if _, err := os.Stat(filepath.Join(modRoot, "vendor")); err == nil {
		g.check(os.Symlink(filepath.Join(modRoot, "vendor"), filepath.Join(tree, "vendor")))
	}
	g.verbosef("Copied the module to %s", tree)
	return tree
}

// writeCoverProfile copies the cover profile that "go test" wrote, without
// the files that gobco injected, and returns the statement coverage.
//
// A block of code appears once for each test binary that contains it,
// and counts as covered if any of them ran it.
func (g *gobco) writeCoverProfile(raw, dst string) *tally {
	data, err := os.ReadFile(raw)
	g.check(err)

	injected := map[string]bool{
		"gobco_fixed.go": true, "gobco_module.go": true, "gobco_variable.go": true,
	}
	var out strings.Builder
	type block struct {
		stmts   int
		covered bool
	}
	blocks := map[string]*block{}
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "mode:") {
			out.WriteString(line + "\n")
			continue
		}
		colon := strings.LastIndex(line, ":")
		file := line[:colon]
		if injected[path.Base(file)] || strings.HasSuffix(file, "/gobcoreg/gobcoreg.go") {
			continue
		}
		out.WriteString(line + "\n")

		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			g.check(fmt.Errorf("gobco: unexpected line %q in the cover profile", line))
		}
		key := file + ":" + fields[0]
		stmts, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		g.check(errors.Join(err1, err2))
		if blocks[key] == nil {
			blocks[key] = &block{stmts: stmts}
			order = append(order, key)
		}
		blocks[key].covered = blocks[key].covered || count > 0
	}
	writeFile(dst, out.String())

	var t tally
	for _, key := range order {
		t.total += blocks[key].stmts
		if blocks[key].covered {
			t.covered += blocks[key].stmts
		}
	}
	return &t
}

// runModuleTests runs the tests of the whole module on the instrumented
// code and returns whether they passed. Each test process saves its counters
// to statsDir.
//
// When a test fails, the coverage is not reported: a test binary that panics
// or times out does not save its counters, and the output of the failed test
// should be the last thing in the log.
func (g *gobco) runModuleTests(overlayFile, statsDir, tree string) bool {
	// "go vet" cannot handle the registry package,
	// which only exists in the overlay.
	extraArgs := []string{"-vet=off"}
	if tree == "" {
		extraArgs = append(extraArgs, "-overlay", overlayFile)
	} else {
		extraArgs = append(extraArgs, "-cover", "-coverprofile="+g.file("cover.out"))
		if g.coverPkg != "" {
			extraArgs = append(extraArgs, "-coverpkg="+g.coverPkg)
		}
	}
	extraArgs = append(extraArgs, g.goTestArgs...)
	args := goTest{}.args(g.verbose, "./...", extraArgs)
	cmd := exec.Command("go", args[1:]...)
	cmd.Dir = tree
	stdout := &testOutput{w: g.stdout, regPath: g.regPath, tree: tree, root: g.modRoot}
	stderr := &testOutput{w: g.stderr, regPath: g.regPath, tree: tree, root: g.modRoot}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "GOBCO_STATS_DIR="+statsDir)

	g.verbosef("Running %q", strings.Join(args, " "))
	err := cmd.Run()
	stdout.flush()
	stderr.flush()
	if err != nil {
		g.errf("go test ./...: %s", err)
		g.errf("gobco: the tests failed, so the coverage is not reported")
		g.exitCode = 1
	}
	return err == nil
}

// testOutput passes the output of "go test" on line by line, without what
// only gobco causes: the registry package, and the coverage that "go test"
// reports for each package, which includes the files that gobco injects.
// In a copy of the module, paths in the output point to the original files.
type testOutput struct {
	w       io.Writer
	regPath string
	tree    string
	root    string
	buf     []byte
}

func (o *testOutput) Write(p []byte) (int, error) {
	o.buf = append(o.buf, p...)
	for {
		i := bytes.IndexByte(o.buf, '\n')
		if i == -1 {
			return len(p), nil
		}
		o.line(string(o.buf[:i+1]))
		o.buf = o.buf[i+1:]
	}
}

func (o *testOutput) flush() {
	if len(o.buf) > 0 {
		o.line(string(o.buf))
		o.buf = nil
	}
}

func (o *testOutput) line(line string) {
	if strings.Contains(line, "\t"+o.regPath+"\t") &&
		(strings.HasPrefix(line, "?") || strings.HasPrefix(line, "\t")) {
		return
	}
	if o.tree != "" {
		if strings.HasPrefix(line, "coverage: ") && strings.Contains(line, "% of statements") {
			return
		}
		line = strings.ReplaceAll(line, o.tree+"/", o.root+"/")
		if i := strings.Index(line, "\tcoverage: "); i != -1 {
			if strings.HasPrefix(line, "\t") {
				// A package without tests, as "go test" prints it
				// when it does not measure the coverage.
				line = "?   " + strings.TrimRight(line[:i], "\t") + "\t[no test files]\n"
			} else {
				line = line[:i] + "\n"
			}
		}
	}
	_, _ = io.WriteString(o.w, line)
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
	if err != nil {
		g.check(fmt.Errorf("gobco: go list: %s", err))
	}
	return strings.TrimSpace(string(out))
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runInModule runs gobco in module mode on testdata/module.
func (s *Suite) runInModule(expectedExitCode int, args ...string) (stdout, stderr string) {
	wd, err := os.Getwd()
	ok(err)
	ok(os.Chdir("testdata/module"))
	defer func() { ok(os.Chdir(wd)) }()

	argv := append([]string{"gobco", "-module"}, args...)
	return s.RunMain(expectedExitCode, argv...)
}

func moduleFiles() []string {
	var files []string
	ok(filepath.Walk("testdata/module", func(path string, _ os.FileInfo, err error) error {
		files = append(files, path)
		return err
	}))
	return files
}

// Package a is covered by its own test and by the test of package it,
// package b has no tests and is covered through package a.
func Test_gobcoMain__module(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()
	before := moduleFiles()

	stdout, _ := s.runInModule(0)

	s.CheckEquals(s.GobcoLines(stdout), []string{
		"Condition coverage: 4/6 (66.7%)",
		"a/a.go:7:5: condition \"x < 0\" was 3 times false but never true",
		"b/b.go:4:14: condition \"x < 100\" was 2 times true but never false",
		"Per-package coverage:",
		"  a: 1/2 (50.0%)",
		"  b: 3/4 (75.0%)",
		"  it: 0/0 (100.0%)",
		"Per-file coverage:",
		"  a/a.go: 1/2 (50.0%)",
		"  b/b.go: 3/4 (75.0%)",
	})
	s.CheckEquals(moduleFiles(), before)
}

func Test_gobcoMain__module_branch(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	stdout, _ := s.runInModule(0, "-branch")

	// In branch mode, "x > 0 && x < 100" counts as a single condition.
	s.CheckContains(stdout, "Branch coverage: 3/4 (75.0%)")
	s.CheckContains(stdout, "  b/b.go: 2/2 (100.0%)")
}

func Test_gobcoMain__module_exclude(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	stdout, _ := s.runInModule(0, "-exclude", "./a")

	s.CheckContains(stdout, "Condition coverage: 3/4 (75.0%)")
	s.CheckNotContains(stdout, "a/a.go")
}

// A pattern that starts with a directory of the module means that
// directory, not an import path from the standard library.
func Test_gobcoMain__module_patterns_without_dot(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	stdout, _ := s.runInModule(0, "-exclude", "a", "a", "b/...", "it")

	s.CheckContains(stdout, "Condition coverage: 3/4 (75.0%)")
	s.CheckNotContains(stdout, "a/a.go")
}

func Test_gobcoMain__module_errors(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	tests := []struct {
		args   []string
		stderr string
	}{
		{[]string{"fmt"}, "/src/fmt is outside the module"},
		{[]string{"-exclude", "nonexistent"}, "gobco: -exclude nonexistent matches no package"},
	}
	for _, test := range tests {
		argv := append([]string{"gobco", "-module"}, test.args...)
		s.CheckPanics(func() { s.runIn("testdata/module", argv...) }, exited(1))
		s.CheckContains(s.Stderr(), test.stderr)
	}
}

// writeModule creates a module in a temporary directory.
//
// The space in its path ensures that gobco keeps such paths intact.
func writeModule(t *testing.T, files map[string]string) string {
	dir := filepath.Join(t.TempDir(), "with space")
	for name, content := range files {
		file := filepath.Join(dir, filepath.FromSlash(name))
		ok(os.MkdirAll(filepath.Dir(file), 0o777))
		writeFile(file, content)
	}
	return dir
}

func (s *Suite) runIn(dir string, argv ...string) int {
	wd, err := os.Getwd()
	ok(err)
	ok(os.Chdir(dir))
	defer func() { ok(os.Chdir(wd)) }()

	return gobcoMain(&s.out, &s.err, argv...)
}

func Test_gobcoMain__module_workspace(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()
	dir := writeModule(t, map[string]string{
		"go.work":   "go 1.18\n\nuse (\n\t./m1\n\t./m2\n)\n",
		"m1/go.mod": "module example.com/m1\n\ngo 1.18\n",
		"m2/go.mod": "module example.com/m2\n\ngo 1.18\n",
	})

	s.CheckPanics(func() { s.runIn(dir, "gobco", "-module") }, exited(1))

	s.CheckEquals(s.Stderr(), "gobco: -module needs exactly one module\n")
}

func Test_gobcoMain__module_registry_exists(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()
	dir := writeModule(t, map[string]string{
		"go.mod":          "module example.com/m\n\ngo 1.16\n",
		"gobcoreg/reg.go": "package gobcoreg\n",
	})

	s.CheckPanics(func() { s.runIn(dir, "gobco", "-module") }, exited(1))

	s.CheckEquals(s.Stderr(), "gobco: directory gobcoreg already exists\n")
}

// A package that does not compile stops the run with the compiler's message.
func Test_gobcoMain__module_build_fails(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.16\n",
		"p.go":   "package p\n\nfunc P() int { return undefined }\n",
	})

	s.CheckPanics(func() { s.runIn(dir, "gobco", "-module") }, exited(1))

	stderr := s.Stderr()
	s.CheckContains(stderr, "p.go:3:23: undefined: undefined")
	s.CheckContains(stderr, "gobco: go list: exit status 1")
}

// When a test fails, its output is the last thing in the log,
// and the coverage is not reported.
func Test_gobcoMain__module_test_fails(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.16\n",
		"p.go": "package p\n\n" +
			"func Abs(x int) int {\n\tif x < 0 {\n\t\treturn -x\n\t}\n\treturn x\n}\n",
		"p_test.go": "package p\n\nimport \"testing\"\n\n" +
			"func TestAbs(t *testing.T) {\n\tif Abs(1) != 2 {\n\t\tt.Error(\"fail\")\n\t}\n}\n",
	})

	exitCode := s.runIn(dir, "gobco", "-module")

	s.CheckEquals(exitCode, 1)
	stdout := s.Stdout()
	s.CheckContains(stdout, "--- FAIL: TestAbs")
	s.CheckNotContains(stdout, "coverage:")
	s.CheckNotContains(stdout, "gobcoreg")
	s.CheckEquals(s.Stderr(), ""+
		"go test ./...: exit status 1\n"+
		"gobco: the tests failed, so the coverage is not reported\n")
}

func Test_gobcoMain__module_threshold(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	_, stderr := s.runInModule(1, "-branch", "-threshold", "80")
	s.CheckContains(stderr, "gobco: coverage 75.0% is below the threshold 80.0%")

	_, stderr = s.runInModule(0, "-branch", "-threshold", "75")
	s.CheckNotContains(stderr, "threshold")
}

func Test_gobcoMain__module_race(t *testing.T) {
	cgo, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	ok(err)
	if strings.TrimSpace(string(cgo)) != "1" {
		t.Skip("the race detector needs cgo")
	}
	s := NewSuite(t)
	defer s.TearDownTest()

	stdout, stderr := s.runInModule(0, "-test", "-race")

	s.CheckNotContains(stdout+stderr, "DATA RACE")
	s.CheckContains(stdout, "Condition coverage: 4/6 (66.7%)")
}

// Only package b is instrumented, and it has no test that would
// persist its counters.
func Test_gobcoMain__module_explicit_packages(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	stdout, _ := s.runInModule(0, "./b")

	s.CheckEquals(s.GobcoLines(stdout), []string{
		"Condition coverage: 0/4 (0.0%)",
		"b/b.go:4:5: condition \"x > 0\" was never evaluated",
		"b/b.go:4:14: condition \"x < 100\" was never evaluated",
		"Per-file coverage:",
		"  b/b.go: 0/4 (0.0%)",
	})
}

func Test_gobcoMain__threshold(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	_, stderr := s.RunMain(1, "gobco", "-threshold", "100", "testdata/issue38")

	s.CheckContains(stderr, "gobco: coverage 50.0% is below the threshold 100.0%")
}

// With a cover profile, the statement coverage is the same as for a plain
// "go test", and the profile contains none of the files that gobco injected.
func Test_gobcoMain__module_coverprofile(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()
	before := moduleFiles()
	profile := filepath.Join(t.TempDir(), "cover.out")

	stdout, _ := s.runInModule(0, "-branch", "-coverprofile", profile, "-coverpkg", "./...")

	plain := filepath.Join(t.TempDir(), "plain.out")
	cmd := exec.Command("go", "test", "-count=1", "-coverpkg=./...", "-coverprofile="+plain, "./...")
	cmd.Dir = "testdata/module"
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %s", err, out)
	}
	g := newGobco(&s.out, &s.err)
	want := g.writeCoverProfile(plain, filepath.Join(t.TempDir(), "plain-filtered.out"))
	s.Stdout()

	s.CheckContains(stdout, "Branch coverage: 3/4 (75.0%)")
	s.CheckContains(stdout, fmt.Sprintf("Statement coverage: %s", want))

	data, err := os.ReadFile(profile)
	ok(err)
	s.CheckNotContains(string(data), "gobco_")
	s.CheckNotContains(string(data), "gobcoreg")
	s.CheckContains(string(data), "mode: set\n")
	s.CheckContains(string(data), "example.com/gobcomod/a/a.go:")
	s.CheckEquals(moduleFiles(), before)
}

func Test_gobcoMain__module_coverprofile_needs_module(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	s.CheckPanics(func() {
		gobcoMain(&s.out, &s.err, "gobco", "-coverprofile", "c.out", "testdata/issue38")
	}, exited(1))
	s.CheckEquals(s.Stderr(), "gobco: -coverprofile needs -module\n")

	s.CheckPanics(func() {
		s.runIn("testdata/module", "gobco", "-module", "-coverpkg", "./...")
	}, exited(1))
	s.CheckEquals(s.Stderr(), "gobco: -coverpkg needs -coverprofile\n")
}

// A black box test may import its package under another name.
func Test_gobcoMain__module_black_box_alias(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.16\n",
		"p/p.go": "package p\n\n" +
			"func Abs(x int) int {\n\tif x < 0 {\n\t\treturn -x\n\t}\n\treturn x\n}\n",
		"p/p_test.go": "package p_test\n\nimport (\n\t\"testing\"\n\n\tother \"example.com/m/p\"\n)\n\n" +
			"func TestAbs(t *testing.T) {\n\tif other.Abs(1) != 1 {\n\t\tt.Error(\"fail\")\n\t}\n}\n",
	})

	exitCode := s.runIn(dir, "gobco", "-module")

	s.CheckEquals(exitCode, 0)
	s.CheckContains(s.Stdout(), "Condition coverage: 1/2 (50.0%)")
	s.CheckEquals(s.Stderr(), "")
}

func Test_testOutput(t *testing.T) {
	s := NewSuite(t)
	defer s.TearDownTest()

	o := testOutput{w: &s.out, regPath: "example.com/m/gobcoreg", tree: "/tmp/tree", root: "/src/m"}
	_, _ = o.Write([]byte("" +
		"?   \texample.com/m/gobcoreg\t[no test files]\n" +
		"\texample.com/m/gobcoreg\t\tcoverage: 0.0% of statements\n" +
		"ok  \texample.com/m/a\t0.010s\tcoverage: 12.3% of statements in ./...\n" +
		"\texample.com/m/b\t\tcoverage: 0.0% of statements\n" +
		"coverage: 12.3% of statements in ./...\n" +
		"\t/tmp/tree/a/a.go:7 +0x10\n" +
		"FAIL\texample.com/m/c\t0.020s\n" +
		"no newline"))
	o.flush()

	s.CheckEquals(s.Stdout(), ""+
		"ok  \texample.com/m/a\t0.010s\n"+
		"?   \texample.com/m/b\t[no test files]\n"+
		"\t/src/m/a/a.go:7 +0x10\n"+
		"FAIL\texample.com/m/c\t0.020s\n"+
		"no newline")

	// Without a copy of the module, only the registry package is left out.
	o = testOutput{w: &s.out, regPath: "example.com/m/gobcoreg"}
	_, _ = o.Write([]byte("" +
		"?   \texample.com/m/gobcoreg\t[no test files]\n" +
		"ok  \texample.com/m/a\t0.010s\tcoverage: 12.3% of statements\n"))

	s.CheckEquals(s.Stdout(), "ok  \texample.com/m/a\t0.010s\tcoverage: 12.3% of statements\n")
}

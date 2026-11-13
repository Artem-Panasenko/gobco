# GOBCO - Golang Branch Coverage

Gobco measures condition coverage of Go code.

This is a fork of [rillig/gobco](https://github.com/rillig/gobco)
that adds a mode covering a whole module in a single test run.

Gobco is intended to be used in addition to `go test -cover`.
For example, gobco does not detect functions or methods that are completely
unused, it only notices them if they contain any conditions or branches.
Gobco also doesn't cover `select` statements.

## Installation

```text
$ go install github.com/artem-panasenko/gobco@latest
```

Gobco needs go1.27 or later.
Build gobco with the Go toolchain of the environment that runs the tests,
since gobco calls `go list` and `go test` from there.

## Usage

To run gobco on a single package, run it in the package directory:

~~~text
$ gobco
~~~

The output typically looks like the following example, taken from package
[github.com/rillig/pkglint](https://github.com/rillig/pkglint):

```text
ok  	github.com/rillig/pkglint/v23	29.648s

Condition coverage: 8720/8840 (98.6%)
...
changes.go:171:61: condition "n == 6" was 12 times true but never false
distinfo.go:268:8: condition "alg == \"SHA1\"" was 16 times false but never true
distinfo.go:322:13: condition "remainingHashes[0].algorithm == alg" was 8 times true but never false
...
mkcondsimplifier.go:141:31: condition "p[2] == p[1]-'A'+'a'" was 26 times true but never false
...
vartypecheck.go:1027:11: condition "len(invalid) > 1" was once false but never true
vartypecheck.go:1628:42: condition "cv.MkLines.pkg != nil" was 8 times true but never false
vartypecheck.go:1630:6: condition "distname.IsConstant()" was 8 times true but never false
```

## Module mode

To cover several packages of a module with a single `go test` run,
run gobco in the module root:

~~~text
$ gobco -module [-branch] [-exclude pattern]... [-threshold pct] [-test opt]... [packages]
~~~

The packages are those that get instrumented, by default `./...`.
The tests of the whole module run once, so a condition counts as covered
no matter which package's test reaches it, including integration tests.
The module is not copied and the working tree is not modified.

A test directory that has its own `TestMain` must be among the packages,
so that it saves the counters when it exits.

The output of `go test` is passed on as it is, so a failing test, a panic or
a timeout reads the same as in a plain `go test` run, and gobco exits with
status 1. In that case gobco does not report the coverage: a test binary that
panics or times out does not save its counters, and the output of the failed
test stays the last thing in the log. A package that does not compile stops
gobco with the message from the compiler, before any test runs.

`-exclude` leaves packages out of the report and the totals,
`-threshold` makes gobco exit with status 1 below the given percentage.

For example:

~~~text
$ gobco -module -branch -exclude ./internal/generated/... ./internal/... ./test/integration
...
Branch coverage: 3/4 (75.0%)
~~~

### Statement coverage in the same run

With `-coverprofile file`, the same test run also measures the statement
coverage, as `go test -cover` does, and gobco writes the profile to `file`:

~~~text
$ gobco -module -branch -coverprofile=cover.out -coverpkg=./internal/... ./internal/... ./test/integration
...
Branch coverage: 3/4 (75.0%)
Statement coverage: 6/7 (85.7%)
~~~

`-coverpkg` is passed on to `go test` and chooses the packages that the
statement coverage counts. The profile has the usual format, so
`go tool cover -func=cover.out` and `go tool cover -html=cover.out` work.
Gobco itself does not write an HTML report.

The Go cover tool reads the source files from the disk and ignores the
overlay, so with `-coverprofile` gobco runs the tests in a copy of the module
(without `.git`, and with `vendor` linked), in a temporary directory that
depends only on the module. The first run compiles everything in that
directory, later runs use the build cache. The copy is removed at the end.
Paths in the output of the tests, such as in a stack trace, point to the
original files. The tests themselves see the copy as their working directory.

The profile leaves out the files that gobco injects, and gobco does not pass
on the coverage that `go test` prints for each package, since that includes
them. Gobco also rewrites some statements, such as `switch`, so the number of
statements can be slightly higher than for a plain `go test`.
Lines are the same as in the source, but columns in lines with a condition are
shifted.

## Ignoring conditions that no test can reach

A condition that starts on the line of a comment `//gobco:ignore`, or on the
line below it, is not instrumented: it is neither counted nor listed.

~~~go
func abs(x int) int {
	//gobco:ignore
	if x > math.MaxInt32 { // The caller never passes such a value.
		panic("unreachable")
	}
	if x == 0 { //gobco:ignore
		return 0
	}
	...
}
~~~

Text may follow the marker after a space, such as the reason.
The marker only affects gobco; the statement coverage of `go test -cover` and
the cover profile stay as they are.

## Adding custom test conditions

If you want to ensure that the tests cover a certain condition in your code,
you can insert the desired condition into the code
and assign it to the underscore:

~~~go
func square(x int) int {
    _ = x > 50
    _ = x == 0
    _ = x < 0

    return x * x
}
~~~

The compiler will see that these conditions are side-effect-free and will thus
optimize them away, so there is no runtime overhead.

Gobco only inserts its coverage code around expressions that are syntactically
recognizable as boolean expressions, such as comparisons, '&&', '||', '!'.
When a boolean expression is merely passed around, there is no branch 
involved, thus nothing to do for branch coverage.

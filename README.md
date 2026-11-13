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

`-exclude` leaves packages out of the report and the totals,
`-threshold` makes gobco exit with status 1 below the given percentage.

For example:

~~~text
$ gobco -module -branch -exclude ./internal/generated/... ./internal/... ./test/integration
...
Branch coverage: 3/4 (75.0%)
~~~

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

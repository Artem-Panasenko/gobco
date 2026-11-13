//go:build ignore
// +build ignore

// This is the part of the gobco code that is injected in module mode,
// where a test binary persists the counters of every instrumented package
// that is linked into it.

package main

import (
	"os"
	"strconv"

	gobcoreg "GOBCOREG"
)

func init() { gobcoreg.Register(gobcoCounts.persist) }

// Each test process saves the counters of each package to a file of its own,
// named "<package>.<pid>.json". Gobco chooses the name of the package.
func (st *gobcoStats) filename() string {
	dir := os.Getenv("GOBCO_STATS_DIR")
	if dir == "" {
		panic("gobco: GOBCO_STATS_DIR environment variable must be set")
	}
	return dir + "/" + "GOBCOSTATS" + "." + strconv.Itoa(os.Getpid()) + ".json"
}

func (st *gobcoStats) finish(exitCode int) int {
	gobcoreg.Flush()
	return exitCode
}

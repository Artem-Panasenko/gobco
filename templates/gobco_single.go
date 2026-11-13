//go:build ignore
// +build ignore

// This is the part of the gobco code that is injected when a single
// package is checked.

package main

import "os"

func (st *gobcoStats) filename() string {
	filename := os.Getenv("GOBCO_STATS")
	if filename == "" {
		panic("gobco: GOBCO_STATS environment variable must be set")
	}
	return filename
}

func (st *gobcoStats) finish(exitCode int) int {
	st.persist()
	return exitCode
}

package it

import (
	"os"
	"testing"

	"example.com/gobcomod/a"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestCheck(t *testing.T) {
	if !a.Check(0) {
		t.Error("want true")
	}
}

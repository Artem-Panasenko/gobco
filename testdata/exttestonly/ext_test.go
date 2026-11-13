package exttestonly_test

import "testing"

func TestNothing(t *testing.T) {
	if len("x") != 1 {
		t.Error("fail")
	}
}

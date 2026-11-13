package testmainreturn

import (
	"fmt"
	"testing"
)

// A TestMain function that does not call os.Exit. Since go1.15, the test
// binary exits with the result of m.Run once TestMain returns.

func TestMain(m *testing.M) {
	fmt.Println("begin original TestMain")
	defer fmt.Println("deferred in TestMain")
	m.Run()
}

func TestPositive(t *testing.T) {
	if !isPositive(3) {
		t.Errorf("3 must be positive")
	}
}

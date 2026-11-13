package ignore

import "testing"

func TestAbs(t *testing.T) {
	if Abs(-1) != 1 || Abs(3) != 3 {
		t.Error("fail")
	}
}

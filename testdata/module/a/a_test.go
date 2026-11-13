package a

import "testing"

func TestCheck(t *testing.T) {
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if !Check(5) {
				t.Error("want true")
			}
		})
	}
}

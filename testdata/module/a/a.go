package a

import "example.com/gobcomod/b"

func Check(x int) bool {
	b.Positive(x)
	if x < 0 {
		return false
	}
	return true
}

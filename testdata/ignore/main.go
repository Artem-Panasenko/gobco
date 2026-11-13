package ignore

func Abs(x int) int {
	//gobco:ignore
	if x > 1000 {
		panic("unreachable")
	}
	if x < 0 {
		return -x
	}
	if x == 7 { //gobco:ignore
		return 7
	}
	if x == 8 { //gobco:ignorefoo is no marker
		return 8
	}
	return x
}

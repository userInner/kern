package page

func Next(current, size, total int) (int, bool) {
	if size <= 0 || total <= 0 {
		return 0, false
	}
	if (current+1)*size <= total {
		return current + 1, true
	}
	return 0, false
}

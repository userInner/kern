package window

// LastN returns the final count values without modifying the input.
func LastN(values []int, count int) []int {
	if count <= 0 || count >= len(values) {
		return nil
	}
	result := make([]int, count)
	copy(result, values[len(values)-count:])
	return result
}

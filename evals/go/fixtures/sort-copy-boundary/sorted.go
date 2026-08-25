package sorted

import "sort"

func Strings(values []string) []string {
	sort.Strings(values)
	return values
}

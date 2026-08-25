package clone

func Clone(source map[string][]string) map[string][]string {
	if source == nil {
		return nil
	}
	result := make(map[string][]string, len(source))
	for key, values := range source {
		result[key] = values
	}
	return result
}

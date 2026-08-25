package snapshot

// Snapshot returns an independent view of values.
func Snapshot(values []string) []string {
	return values[:]
}

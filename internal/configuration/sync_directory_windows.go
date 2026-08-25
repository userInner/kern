//go:build windows

package configuration

// Windows does not support syncing directory handles through os.File.Sync.
// The temporary configuration file itself is synced before the atomic rename.
func syncDirectory(string) error {
	return nil
}

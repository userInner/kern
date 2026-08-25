package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/store/sqlite"
)

func TestManagerInstallEnableDisableRemove(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := filepath.Join(t.TempDir(), "plugins")
	manager, err := New(root, store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	source := createPluginPackage(t, "dev.kern.test")
	installed, created, err := manager.Install(t.Context(), source)
	if err != nil || !created || installed.Enabled {
		t.Fatalf("Install() = %#v, %v, %v", installed, created, err)
	}
	if installed.InstallPath == source || !filepath.IsAbs(installed.InstallPath) {
		t.Fatalf("InstallPath = %q", installed.InstallPath)
	}
	if _, err := os.Stat(filepath.Join(installed.InstallPath, "knowledge", "principles.md")); err != nil {
		t.Fatalf("installed payload: %v", err)
	}
	repeated, created, err := manager.Install(t.Context(), source)
	if err != nil || created || repeated.ID != installed.ID {
		t.Fatalf("Install(repeat) = %#v, %v, %v", repeated, created, err)
	}
	enabled, err := manager.Enable(t.Context(), installed.ID)
	if err != nil || !enabled.Enabled {
		t.Fatalf("Enable() = %#v, %v", enabled, err)
	}
	disabled, err := manager.Disable(t.Context(), installed.ID)
	if err != nil || disabled.Enabled {
		t.Fatalf("Disable() = %#v, %v", disabled, err)
	}
	if err := manager.Remove(t.Context(), installed.ID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := manager.Get(t.Context(), installed.ID); !errors.Is(err, plugin.ErrNotFound) {
		t.Fatalf("Get(removed) error = %v", err)
	}
	if _, err := os.Stat(installed.InstallPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed path after remove error = %v", err)
	}
}

func TestManagerCloseIsIdempotentAndRejectsFurtherOperations(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	source := createPluginPackage(t, "dev.kern.closed")
	importRoot := openTestRoot(t, t.TempDir())

	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close(repeat) error = %v", err)
	}
	if _, err := manager.storage.Stat("."); err == nil {
		t.Fatal("storage.Stat() after Close error = nil")
	}
	if err := (*Manager)(nil).Close(); err != nil {
		t.Fatalf("(*Manager)(nil).Close() error = %v", err)
	}

	assertClosed := func(operation string, err error) {
		t.Helper()
		if !errors.Is(err, ErrClosed) {
			t.Errorf("%s error = %v, want ErrClosed", operation, err)
		}
	}
	_, _, err = manager.Install(t.Context(), source)
	assertClosed("Install", err)
	_, _, err = manager.InstallFromRoot(t.Context(), importRoot, "package")
	assertClosed("InstallFromRoot", err)
	_, err = manager.Get(t.Context(), "dev.kern.closed")
	assertClosed("Get", err)
	_, err = manager.List(t.Context())
	assertClosed("List", err)
	_, err = manager.Enable(t.Context(), "dev.kern.closed")
	assertClosed("Enable", err)
	_, err = manager.Disable(t.Context(), "dev.kern.closed")
	assertClosed("Disable", err)
	assertClosed("Remove", manager.Remove(t.Context(), "dev.kern.closed"))
}

func TestManagerCloseWaitsForActiveOperations(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.beginOperation(); err != nil {
		t.Fatalf("beginOperation() error = %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close() returned while operation active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	manager.endOperation()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not return after active operation ended")
	}
}

func TestManagerConcurrentClose(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsSeen <- manager.Close()
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Close() error = %v", err)
		}
	}
}

func TestManagerRefusesToEnableTamperedInstalledPackage(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	installed, _, err := manager.Install(t.Context(), createPluginPackage(t, "dev.kern.tamper"))
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(installed.InstallPath, "knowledge", "principles.md"),
		[]byte("tampered"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(tampered) error = %v", err)
	}
	if _, err := manager.Enable(t.Context(), installed.ID); !errors.Is(err, plugin.ErrIntegrity) {
		t.Fatalf("Enable(tampered) error = %v", err)
	}
}

func TestManagerInstallFromRoot(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	imports := t.TempDir()
	source := filepath.Join(imports, "packages", "go")
	writePluginPackage(t, source, "dev.kern.rooted")
	importRoot, err := os.OpenRoot(imports)
	if err != nil {
		t.Fatalf("OpenRoot(imports) error = %v", err)
	}
	defer importRoot.Close()

	installed, created, err := manager.InstallFromRoot(t.Context(), importRoot, "packages/go")
	if err != nil || !created {
		t.Fatalf("InstallFromRoot() = %#v, %v, %v", installed, created, err)
	}
	if installed.ID != "dev.kern.rooted" {
		t.Fatalf("installed ID = %q, want dev.kern.rooted", installed.ID)
	}
	if _, err := os.Stat(filepath.Join(installed.InstallPath, "knowledge", "principles.md")); err != nil {
		t.Fatalf("installed payload: %v", err)
	}
}

func TestManagerInstallFromRootRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	importRoot, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("OpenRoot(imports) error = %v", err)
	}
	defer importRoot.Close()

	for _, source := range []string{
		"",
		"/absolute",
		"../outside",
		`..\outside`,
		"package\x00name",
		"C:/Windows/system32",
		"C:drive-relative",
	} {
		if _, _, err := manager.InstallFromRoot(t.Context(), importRoot, source); err == nil {
			t.Errorf("InstallFromRoot(%q) error = nil, want unsafe-path rejection", source)
		}
	}
}

func TestManagerInstallFromRootClassifiesInvalidPackages(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	imports := t.TempDir()
	if err := os.Mkdir(filepath.Join(imports, "missing-manifest"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	importRoot, err := os.OpenRoot(imports)
	if err != nil {
		t.Fatalf("OpenRoot(imports) error = %v", err)
	}
	defer importRoot.Close()

	if _, _, err := manager.InstallFromRoot(t.Context(), importRoot, "missing-manifest"); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("InstallFromRoot(missing manifest) error = %v, want ErrInvalidSource", err)
	}
}

func TestManagerInstallFromRootRejectsSymbolicLinkSources(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	imports := t.TempDir()
	importRoot, err := os.OpenRoot(imports)
	if err != nil {
		t.Fatalf("OpenRoot(imports) error = %v", err)
	}
	defer importRoot.Close()

	t.Run("final", func(t *testing.T) {
		outside := t.TempDir()
		writePluginPackage(t, outside, "dev.kern.final-link")
		if err := os.Symlink(outside, filepath.Join(imports, "alias")); err != nil {
			t.Skipf("Symlink() unavailable: %v", err)
		}
		if _, _, err := manager.InstallFromRoot(t.Context(), importRoot, "alias"); err == nil ||
			!strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("InstallFromRoot(final symlink) error = %v, want symbolic-link rejection", err)
		}
	})

	t.Run("intermediate", func(t *testing.T) {
		outside := t.TempDir()
		writePluginPackage(t, filepath.Join(outside, "package"), "dev.kern.intermediate-link")
		if err := os.Symlink(outside, filepath.Join(imports, "group")); err != nil {
			t.Skipf("Symlink() unavailable: %v", err)
		}
		if _, _, err := manager.InstallFromRoot(t.Context(), importRoot, "group/package"); err == nil ||
			!strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("InstallFromRoot(intermediate symlink) error = %v, want symbolic-link rejection", err)
		}
	})
}

func TestManagerRejectsStorageAndSourceOverlap(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		layout func(t *testing.T) (storage, source string)
	}{
		{
			name: "same directory",
			layout: func(t *testing.T) (string, string) {
				storage := filepath.Join(t.TempDir(), "plugins")
				return storage, storage
			},
		},
		{
			name: "storage below source",
			layout: func(t *testing.T) (string, string) {
				source := t.TempDir()
				return filepath.Join(source, "storage"), source
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, source := test.layout(t)
			store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
			if err != nil {
				t.Fatalf("sqlite.Open() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			manager, err := New(storage, store)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			t.Cleanup(func() { _ = manager.Close() })
			writePluginPackage(t, source, "dev.kern.overlap")
			if _, _, err := manager.Install(t.Context(), source); err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("Install(overlap) error = %v, want overlap rejection", err)
			}
			matches, err := filepath.Glob(filepath.Join(storage, ".install-*"))
			if err != nil {
				t.Fatalf("Glob(staging) error = %v", err)
			}
			if len(matches) != 0 {
				t.Fatalf("overlap install created staging directories: %v", matches)
			}
		})
	}

	t.Run("symbolic link alias", func(t *testing.T) {
		store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
		if err != nil {
			t.Fatalf("sqlite.Open() error = %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		storage := filepath.Join(t.TempDir(), "plugins")
		manager, err := New(storage, store)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		writePluginPackage(t, storage, "dev.kern.alias-overlap")
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(storage, alias); err != nil {
			t.Skipf("Symlink() unavailable: %v", err)
		}
		if _, _, err := manager.Install(t.Context(), alias); err == nil || !strings.Contains(err.Error(), "overlap") {
			t.Fatalf("Install(alias overlap) error = %v, want overlap rejection", err)
		}
	})

	t.Run("case-folded ancestor alias", func(t *testing.T) {
		base := t.TempDir()
		storage := filepath.Join(base, "Plugins")
		store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
		if err != nil {
			t.Fatalf("sqlite.Open() error = %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		manager, err := New(storage, store)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		source := filepath.Join(base, "plugins", "source")
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatalf("MkdirAll(case-folded source) error = %v", err)
		}
		storageInfo, storageErr := os.Stat(storage)
		aliasInfo, aliasErr := os.Stat(filepath.Join(base, "plugins"))
		if storageErr != nil || aliasErr != nil || !os.SameFile(storageInfo, aliasInfo) {
			t.Skip("filesystem is case-sensitive")
		}
		writePluginPackage(t, source, "dev.kern.case-alias")
		if _, _, err := manager.Install(t.Context(), source); err == nil || !strings.Contains(err.Error(), "overlap") {
			t.Fatalf("Install(case-folded overlap) error = %v, want identity-based rejection", err)
		}
	})
}

func TestManagerInstallFromRootRejectsStorageOverlap(t *testing.T) {
	t.Parallel()

	t.Run("same directory", func(t *testing.T) {
		imports := t.TempDir()
		storage := filepath.Join(imports, "plugins")
		store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
		if err != nil {
			t.Fatalf("sqlite.Open() error = %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		manager, err := New(storage, store)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		writePluginPackage(t, storage, "dev.kern.rooted-overlap")
		importRoot, err := os.OpenRoot(imports)
		if err != nil {
			t.Fatalf("OpenRoot(imports) error = %v", err)
		}
		defer importRoot.Close()

		if _, _, err := manager.InstallFromRoot(t.Context(), importRoot, "plugins"); err == nil ||
			!strings.Contains(err.Error(), "overlap") {
			t.Fatalf("InstallFromRoot(overlap) error = %v, want overlap rejection", err)
		}
	})

	t.Run("trusted root aliases storage", func(t *testing.T) {
		storage := filepath.Join(t.TempDir(), "plugins")
		store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
		if err != nil {
			t.Fatalf("sqlite.Open() error = %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		manager, err := New(storage, store)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		writePluginPackage(t, storage, "dev.kern.rooted-alias-overlap")
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(storage, alias); err != nil {
			t.Skipf("Symlink() unavailable: %v", err)
		}
		importRoot, err := os.OpenRoot(alias)
		if err != nil {
			t.Fatalf("OpenRoot(alias) error = %v", err)
		}
		defer importRoot.Close()

		if _, _, err := manager.InstallFromRoot(t.Context(), importRoot, "."); err == nil ||
			!strings.Contains(err.Error(), "overlap") {
			t.Fatalf("InstallFromRoot(alias overlap) error = %v, want overlap rejection", err)
		}
	})
}

func TestManagerRejectsReplacedStorageRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	storage := filepath.Join(base, "plugins")
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(storage, store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	moved := filepath.Join(base, "plugins-moved")
	if err := os.Rename(storage, moved); err != nil {
		t.Skipf("platform does not permit renaming an open directory: %v", err)
	}
	if err := os.Mkdir(storage, 0o700); err != nil {
		t.Fatalf("Mkdir(replacement) error = %v", err)
	}

	if _, _, err := manager.Install(t.Context(), createPluginPackage(t, "dev.kern.replaced-root")); err == nil ||
		!strings.Contains(err.Error(), "storage root changed") {
		t.Fatalf("Install(replaced storage) error = %v, want stable-root rejection", err)
	}
	entries, err := os.ReadDir(storage)
	if err != nil {
		t.Fatalf("ReadDir(replacement) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement storage received files: %v", entries)
	}
}

func TestManagerRejectsSymlinkedDestinationParent(t *testing.T) {
	t.Parallel()

	storage := filepath.Join(t.TempDir(), "plugins")
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(storage, store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(storage, "dev.kern.destination-link")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	if _, _, err := manager.Install(t.Context(), createPluginPackage(t, "dev.kern.destination-link")); err == nil ||
		!strings.Contains(err.Error(), "regular directory") {
		t.Fatalf("Install(symlinked destination parent) error = %v, want rejection", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("ReadDir(outside) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory received files: %v", entries)
	}
}

func TestManagerRejectsPublishedManifestMismatch(t *testing.T) {
	t.Parallel()

	storage := filepath.Join(t.TempDir(), "plugins")
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(storage, store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	source := createPluginPackage(t, "dev.kern.manifest-race")
	manifest, _, err := plugin.LoadManifest(source)
	if err != nil {
		t.Fatalf("LoadManifest(source) error = %v", err)
	}
	destination := filepath.Join(
		storage,
		manifest.ID,
		manifest.Version,
		strings.TrimPrefix(manifest.Integrity.Files, "sha256:"),
	)
	writePluginPackage(t, destination, manifest.ID)
	data, err := os.ReadFile(filepath.Join(destination, plugin.ManifestFile))
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	var injected plugin.Manifest
	if err := json.Unmarshal(data, &injected); err != nil {
		t.Fatalf("Unmarshal(manifest) error = %v", err)
	}
	injected.Name = "Injected Name"
	encoded, err := json.Marshal(injected)
	if err != nil {
		t.Fatalf("Marshal(injected manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(injected manifest) error = %v", err)
	}

	if _, _, err := manager.Install(t.Context(), source); !errors.Is(err, plugin.ErrIntegrity) ||
		!strings.Contains(err.Error(), "published manifest differs") {
		t.Fatalf("Install(manifest mismatch) error = %v, want integrity rejection", err)
	}
}

func TestCopyPackageEnforcesLimitsAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("cancelled", func(t *testing.T) {
		source := openTestRoot(t, t.TempDir())
		destination := openTestRoot(t, t.TempDir())
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := copyPackage(ctx, source, destination); !errors.Is(err, context.Canceled) {
			t.Fatalf("copyPackage(cancelled) error = %v, want context.Canceled", err)
		}
	})

	t.Run("file count", func(t *testing.T) {
		sourcePath := t.TempDir()
		for index := range plugin.MaxPackageFiles + 1 {
			name := filepath.Join(sourcePath, fmt.Sprintf("payload-%04d", index))
			if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
				t.Fatalf("WriteFile(%d) error = %v", index, err)
			}
		}
		source := openTestRoot(t, sourcePath)
		destination := openTestRoot(t, t.TempDir())
		if err := copyPackage(t.Context(), source, destination); !errors.Is(err, ErrInvalidSource) ||
			!strings.Contains(err.Error(), "file limit") {
			t.Fatalf("copyPackage(too many files) error = %v, want limit rejection", err)
		}
	})

	t.Run("byte count", func(t *testing.T) {
		sourcePath := t.TempDir()
		file, err := os.Create(filepath.Join(sourcePath, "oversized.bin"))
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := file.Truncate(int64(plugin.MaxPackageBytes) + 1); err != nil {
			t.Fatalf("Truncate() error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		source := openTestRoot(t, sourcePath)
		destination := openTestRoot(t, t.TempDir())
		if err := copyPackage(t.Context(), source, destination); !errors.Is(err, ErrInvalidSource) ||
			!strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("copyPackage(oversized) error = %v, want limit rejection", err)
		}
	})
}

func openTestRoot(t *testing.T, directory string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot(%q) error = %v", directory, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func createPluginPackage(t *testing.T, pluginID string) string {
	t.Helper()
	directory := t.TempDir()
	writePluginPackage(t, directory, pluginID)
	return directory
}

func writePluginPackage(t *testing.T, directory, pluginID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(directory, "knowledge"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "knowledge", "principles.md"),
		[]byte("Use deterministic evidence.\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(payload) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            pluginID,
		Name:          "Test Expert",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{
			Knowledge: []string{"knowledge/principles.md"},
		},
		Activation: plugin.Activation{Signals: []string{"go.mod"}, Intents: []string{"code.review"}},
		Permissions: plugin.Permissions{
			Filesystem: []string{"workspace:read"},
		},
		Integrity: plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
}

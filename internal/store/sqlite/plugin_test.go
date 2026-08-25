package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/userInner/kern/internal/plugin"
)

func TestStorePluginLifecycleAndVersionLock(t *testing.T) {
	store := openTestStore(t)
	item := testInstalledPlugin()
	installed, created, err := store.InstallPlugin(t.Context(), item)
	if err != nil || !created || installed.ID != item.ID || installed.Enabled {
		t.Fatalf("InstallPlugin() = %#v, %v, %v", installed, created, err)
	}
	repeated, created, err := store.InstallPlugin(t.Context(), item)
	if err != nil || created || repeated.Digest != item.Digest {
		t.Fatalf("InstallPlugin(repeat) = %#v, %v, %v", repeated, created, err)
	}
	conflict := item
	conflict.Version = "0.2.0"
	conflict.Manifest.Version = conflict.Version
	if _, _, err := store.InstallPlugin(t.Context(), conflict); !errors.Is(err, plugin.ErrConflict) {
		t.Fatalf("InstallPlugin(conflict) error = %v", err)
	}
	enabled, err := store.SetPluginEnabled(t.Context(), item.ID, true)
	if err != nil || !enabled.Enabled {
		t.Fatalf("SetPluginEnabled() = %#v, %v", enabled, err)
	}
	items, err := store.ListPlugins(t.Context())
	if err != nil || len(items) != 1 || !items[0].Enabled {
		t.Fatalf("ListPlugins() = %#v, %v", items, err)
	}
	if err := store.DeletePlugin(t.Context(), item.ID); err != nil {
		t.Fatalf("DeletePlugin() error = %v", err)
	}
	if _, err := store.GetPlugin(t.Context(), item.ID); !errors.Is(err, plugin.ErrNotFound) {
		t.Fatalf("GetPlugin(deleted) error = %v", err)
	}
}

func testInstalledPlugin() plugin.Installed {
	digest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.test",
		Name:          "Test Expert",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints:   plugin.Entrypoints{Knowledge: []string{"knowledge.md"}},
		Integrity:     plugin.Integrity{Files: digest},
	}
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID,
		Name:          manifest.Name,
		Version:       manifest.Version,
		Source:        "/local/test",
		Digest:        digest,
		TrustStatus:   "local-unverified",
		Manifest:      manifest,
		InstallPath:   "/data/plugins/dev.kern.test",
		InstalledAt:   time.Now().UTC(),
	}
}

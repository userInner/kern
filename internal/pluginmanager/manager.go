// Package pluginmanager owns safe local plugin installation and lifecycle.
package pluginmanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/userInner/kern/internal/plugin"
)

type repository interface {
	InstallPlugin(ctx context.Context, item plugin.Installed) (plugin.Installed, bool, error)
	GetPlugin(ctx context.Context, pluginID string) (plugin.Installed, error)
	ListPlugins(ctx context.Context) ([]plugin.Installed, error)
	SetPluginEnabled(ctx context.Context, pluginID string, enabled bool) (plugin.Installed, error)
	DeletePlugin(ctx context.Context, pluginID string) error
}

// Manager owns plugin files beneath one private root.
type Manager struct {
	root string
	repo repository
}

// New constructs a plugin manager and restricts its storage directory.
func New(root string, repo repository) (*Manager, error) {
	if strings.TrimSpace(root) == "" || repo == nil {
		return nil, errors.New("pluginmanager: root and repository are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("pluginmanager: resolving root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("pluginmanager: creating root: %w", err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("pluginmanager: restricting root: %w", err)
	}
	return &Manager{root: absolute, repo: repo}, nil
}

// Install validates, verifies, copies, and durably records a local package.
func (m *Manager) Install(ctx context.Context, source string) (plugin.Installed, bool, error) {
	if err := ctx.Err(); err != nil {
		return plugin.Installed{}, false, err
	}
	source, err := filepath.Abs(strings.TrimSpace(source))
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: resolving source: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: reading source: %w", err)
	}
	if !info.IsDir() {
		return plugin.Installed{}, false, errors.New("pluginmanager: source must be a directory")
	}
	if relative, err := filepath.Rel(source, m.root); err == nil && relative != "." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return plugin.Installed{}, false, errors.New("pluginmanager: storage root must not be inside source")
	}
	manifest, _, err := plugin.LoadManifest(source)
	if err != nil {
		return plugin.Installed{}, false, err
	}
	digest, err := plugin.VerifyPackage(source, manifest)
	if err != nil {
		return plugin.Installed{}, false, err
	}
	if existing, err := m.repo.GetPlugin(ctx, manifest.ID); err == nil {
		if existing.Version != manifest.Version || existing.Digest != digest {
			return plugin.Installed{}, false, plugin.ErrConflict
		}
		if _, verifyErr := plugin.VerifyPackage(existing.InstallPath, existing.Manifest); verifyErr != nil {
			return plugin.Installed{}, false, fmt.Errorf("pluginmanager: installed package is corrupt: %w", verifyErr)
		}
		return existing, false, nil
	} else if !errors.Is(err, plugin.ErrNotFound) {
		return plugin.Installed{}, false, err
	}

	destination := filepath.Join(
		m.root,
		manifest.ID,
		manifest.Version,
		strings.TrimPrefix(digest, "sha256:"),
	)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: creating package parent: %w", err)
	}
	staging, err := os.MkdirTemp(m.root, ".install-*")
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: creating staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := copyPackage(ctx, source, staging); err != nil {
		return plugin.Installed{}, false, err
	}
	if copiedDigest, err := plugin.VerifyPackage(staging, manifest); err != nil || copiedDigest != digest {
		return plugin.Installed{}, false, errors.Join(plugin.ErrIntegrity, err)
	}
	createdFiles := false
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(staging, destination); err != nil {
			return plugin.Installed{}, false, fmt.Errorf("pluginmanager: publishing package: %w", err)
		}
		createdFiles = true
	} else if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: checking destination: %w", err)
	}
	if publishedDigest, err := plugin.VerifyPackage(destination, manifest); err != nil || publishedDigest != digest {
		if createdFiles {
			_ = os.RemoveAll(destination)
		}
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: verifying published package: %w", errors.Join(plugin.ErrIntegrity, err))
	}
	now := time.Now().UTC()
	item := plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID,
		Name:          manifest.Name,
		Description:   manifest.Description,
		Version:       manifest.Version,
		Source:        source,
		Digest:        digest,
		Enabled:       false,
		TrustStatus:   "local-unverified",
		Manifest:      manifest,
		InstalledAt:   now,
		UpdatedAt:     now,
		InstallPath:   destination,
	}
	installed, created, err := m.repo.InstallPlugin(ctx, item)
	if err != nil {
		if createdFiles {
			_ = os.RemoveAll(destination)
		}
		return plugin.Installed{}, false, err
	}
	return installed, created, nil
}

func (m *Manager) Get(ctx context.Context, pluginID string) (plugin.Installed, error) {
	return m.repo.GetPlugin(ctx, pluginID)
}

func (m *Manager) List(ctx context.Context) ([]plugin.Installed, error) {
	return m.repo.ListPlugins(ctx)
}

func (m *Manager) Enable(ctx context.Context, pluginID string) (plugin.Installed, error) {
	item, err := m.repo.GetPlugin(ctx, pluginID)
	if err != nil {
		return plugin.Installed{}, err
	}
	if _, err := plugin.VerifyPackage(item.InstallPath, item.Manifest); err != nil {
		return plugin.Installed{}, fmt.Errorf("pluginmanager: refusing to enable corrupt package: %w", err)
	}
	return m.repo.SetPluginEnabled(ctx, pluginID, true)
}

func (m *Manager) Disable(ctx context.Context, pluginID string) (plugin.Installed, error) {
	return m.repo.SetPluginEnabled(ctx, pluginID, false)
}

// Remove quarantines exact package bytes before deleting the durable record.
func (m *Manager) Remove(ctx context.Context, pluginID string) error {
	item, err := m.repo.GetPlugin(ctx, pluginID)
	if err != nil {
		return err
	}
	cleanPath := filepath.Clean(item.InstallPath)
	relative, err := filepath.Rel(m.root, cleanPath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("pluginmanager: refusing to remove path outside plugin root")
	}
	quarantine, err := os.MkdirTemp(m.root, ".remove-*")
	if err != nil {
		return fmt.Errorf("pluginmanager: creating removal quarantine: %w", err)
	}
	if err := os.Remove(quarantine); err != nil {
		return fmt.Errorf("pluginmanager: preparing removal quarantine: %w", err)
	}
	if err := os.Rename(cleanPath, quarantine); err != nil {
		return fmt.Errorf("pluginmanager: quarantining package: %w", err)
	}
	if err := m.repo.DeletePlugin(ctx, pluginID); err != nil {
		rollbackErr := os.Rename(quarantine, cleanPath)
		return errors.Join(err, rollbackErr)
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("pluginmanager: deleting quarantined package: %w", err)
	}
	removeEmptyParents(filepath.Dir(cleanPath), m.root)
	return nil
}

func copyPackage(ctx context.Context, source, destination string) error {
	return filepath.WalkDir(source, func(sourcePath string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, sourcePath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("pluginmanager: symbolic links are not allowed: %s", relative)
		}
		target := filepath.Join(destination, relative)
		if item.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !item.Type().IsRegular() {
			return fmt.Errorf("pluginmanager: non-regular file is not allowed: %s", relative)
		}
		input, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, output.Sync(), output.Close(), input.Close())
	})
}

func removeEmptyParents(directory, stop string) {
	for directory != stop {
		if err := os.Remove(directory); err != nil {
			return
		}
		directory = filepath.Dir(directory)
	}
}

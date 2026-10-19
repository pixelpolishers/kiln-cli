// Package localrepo is the "local" provider: a directory that holds built
// packages in the layout
//
//	<root>/<vendor>/<name>/<version>/kiln-assets.json
//	<root>/<vendor>/<name>/<version>/<archive files and <id>.asset.json sidecars>
//
// File URLs in the asset manifest are paths relative to its directory.
package localrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/archive"
	"github.com/pixelpolishers/kiln-core/manifest"
)

const (
	AssetManifestFile = "kiln-assets.json"
	SidecarSuffix     = ".asset.json"
)

var (
	// ErrNoAssets means the repository has nothing published for that version.
	ErrNoAssets = errors.New("no assets published")
	// ErrNoAsset means the version is published but has no binary with that id.
	ErrNoAsset = errors.New("no matching asset")
)

type Repo struct {
	Root      string
	Validator *manifest.Validator
}

func (r Repo) dir(pkg, version string) string {
	return filepath.Join(r.Root, filepath.FromSlash(pkg), version)
}

// Manifest reads and validates the asset manifest of pkg@version.
func (r Repo) Manifest(pkg, version string) (manifest.AssetManifest, error) {
	raw, err := os.ReadFile(filepath.Join(r.dir(pkg, version), AssetManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return manifest.AssetManifest{}, fmt.Errorf("%s@%s: %w", pkg, version, ErrNoAssets)
	}
	if err != nil {
		return manifest.AssetManifest{}, err
	}
	m, err := r.Validator.ParseAssets(raw)
	if err != nil {
		return manifest.AssetManifest{}, fmt.Errorf("%s@%s: %w", pkg, version, err)
	}
	if m.Package != pkg || m.Version != version {
		return manifest.AssetManifest{}, fmt.Errorf("%s@%s: asset manifest is for %s@%s", pkg, version, m.Package, m.Version)
	}
	return m, nil
}

// Find returns the asset with the given id and the path of its file.
func (r Repo) Find(pkg, version, id string) (manifest.Asset, string, error) {
	m, err := r.Manifest(pkg, version)
	if err != nil {
		return manifest.Asset{}, "", err
	}
	for _, a := range m.Assets {
		if a.ID != id {
			continue
		}
		p, err := r.filePath(pkg, version, a.File.URL)
		return a, p, err
	}
	return manifest.Asset{}, "", fmt.Errorf("%s@%s: %w for id %s", pkg, version, ErrNoAsset, id)
}

func (r Repo) filePath(pkg, version, rel string) (string, error) {
	dir := r.dir(pkg, version)
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if within, err := filepath.Rel(dir, p); err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("asset file %q points outside the package directory", rel)
	}
	return p, nil
}

// Add copies a packaged archive and its sidecar into the repository. It is
// idempotent: adding the same id with the same content again does nothing,
// while a different file under an existing id is an error.
func (r Repo) Add(pkg, version, archivePath, sidecarPath string) (added bool, err error) {
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		return false, err
	}
	var a manifest.Asset
	if err := json.Unmarshal(raw, &a); err != nil {
		return false, fmt.Errorf("%s: %w", sidecarPath, err)
	}
	if got := filepath.Base(archivePath); got != a.File.URL {
		return false, fmt.Errorf("sidecar %s refers to %q but the archive is %q", filepath.Base(sidecarPath), a.File.URL, got)
	}
	sum, _, err := archive.SHA256(archivePath)
	if err != nil {
		return false, err
	}
	if sum != a.File.SHA256 {
		return false, fmt.Errorf("%s does not match the hash in its sidecar", filepath.Base(archivePath))
	}

	dir := r.dir(pkg, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	destSidecar := filepath.Join(dir, a.ID+SidecarSuffix)
	if old, err := os.ReadFile(destSidecar); err == nil {
		var prev manifest.Asset
		if json.Unmarshal(old, &prev) == nil && prev.File.SHA256 == a.File.SHA256 {
			return false, nil
		}
		return false, fmt.Errorf("%s@%s already has a different binary for id %s; published binaries are immutable", pkg, version, a.ID)
	}
	if err := copyFile(archivePath, filepath.Join(dir, a.File.URL)); err != nil {
		return false, err
	}
	return true, copyFile(sidecarPath, destSidecar)
}

// Finalize assembles kiln-assets.json from the sidecars next to it.
func (r Repo) Finalize(pkg, version string, src manifest.AssetSource) (int, error) {
	dir := r.dir(pkg, version)
	names, err := filepath.Glob(filepath.Join(dir, "*"+SidecarSuffix))
	if err != nil {
		return 0, err
	}
	if len(names) == 0 {
		return 0, fmt.Errorf("%s@%s: no published assets to finalize in %s", pkg, version, dir)
	}
	sort.Strings(names)
	m := manifest.AssetManifest{Package: pkg, Version: version, Source: src}
	for _, n := range names {
		raw, err := os.ReadFile(n)
		if err != nil {
			return 0, err
		}
		var a manifest.Asset
		if err := json.Unmarshal(raw, &a); err != nil {
			return 0, fmt.Errorf("%s: %w", filepath.Base(n), err)
		}
		m.Assets = append(m.Assets, a)
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, err
	}
	if _, err := r.Validator.ParseAssets(out); err != nil {
		return 0, fmt.Errorf("the assembled asset manifest is invalid: %w", err)
	}
	tmp := filepath.Join(dir, ".kiln-assets.json.tmp")
	if err := os.WriteFile(tmp, append(out, '\n'), 0o644); err != nil {
		return 0, err
	}
	return len(m.Assets), os.Rename(tmp, filepath.Join(dir, AssetManifestFile))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

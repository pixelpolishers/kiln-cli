// Package cache stores unpacked packages keyed by asset id.
package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/archive"
	"github.com/pixelpolishers/kiln-core/manifest"
)

var (
	idRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$`)
	versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]*$`)
)

// shortID is how much of the asset id appears in a directory name. The entry
// file holds the full id and is checked on every use.
const shortID = 12

type Cache struct{ Root string }

// Default returns the cache at $KILN_CACHE_DIR, or the user cache directory.
func Default() (Cache, error) {
	if d := os.Getenv("KILN_CACHE_DIR"); d != "" {
		return Cache{Root: d}, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return Cache{}, fmt.Errorf("no cache directory (set KILN_CACHE_DIR): %w", err)
	}
	return Cache{Root: filepath.Join(d, "kiln")}, nil
}

// Key names one cache entry: a package version built in one exact way.
type Key struct {
	Name    string // vendor/name
	Version string
	ID      string // the full asset id
}

func (k Key) check() error {
	switch {
	case !nameRe.MatchString(k.Name) || strings.Contains(k.Name, ".."):
		return fmt.Errorf("invalid package name %q", k.Name)
	case !versionRe.MatchString(k.Version):
		return fmt.Errorf("invalid version %q", k.Version)
	case !idRe.MatchString(k.ID):
		return fmt.Errorf("invalid asset id %q", k.ID)
	}
	return nil
}

// Dir is where the package build lives (whether or not it is there):
// packages/vendor/name/version/<first 12 characters of the id>.
func (c Cache) Dir(k Key) string {
	short := k.ID
	if len(short) > shortID {
		short = short[:shortID]
	}
	return filepath.Join(c.Root, "packages", filepath.FromSlash(k.Name), k.Version, short)
}

// Has reports whether the entry exists.
func (c Cache) Has(k Key) bool {
	if k.check() != nil {
		return false
	}
	st, err := os.Stat(c.Dir(k))
	return err == nil && st.IsDir()
}

// EntryFile is written into every cache entry; it records how the entry came to be.
const EntryFile = ".kiln-entry.json"

// Entry describes a cache entry.
type Entry struct {
	// Origin is "binary" (downloaded, verified) or "source-build".
	Origin string `json:"origin"`
	// Source is set for source builds: what was built.
	Source *SourceRef `json:"source,omitempty"`
	// Asset is the asset the entry was unpacked from (for source builds, the
	// one that was produced locally).
	Asset manifest.Asset `json:"asset"`
}

type SourceRef struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

// Entry returns the record of a cache entry.
func (c Cache) Entry(k Key) (Entry, error) {
	if err := k.check(); err != nil {
		return Entry{}, err
	}
	raw, err := os.ReadFile(filepath.Join(c.Dir(k), EntryFile))
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Entry{}, err
	}
	if e.Asset.ID != k.ID {
		return Entry{}, fmt.Errorf("the entry in %s is for asset %s, not %s", c.Dir(k), e.Asset.ID, k.ID)
	}
	return e, nil
}

// Put unpacks the archive as the entry for id and records e beside it. The entry appears atomically: it is unpacked next to the
// final location and renamed, so a crash never leaves a half-extracted
// package that looks complete.
func (c Cache) Put(k Key, archivePath string, e Entry) error {
	if err := k.check(); err != nil {
		return err
	}
	final := c.Dir(k)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Root, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(c.Root, ".unpack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := archive.Extract(archivePath, tmp); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, EntryFile), meta, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		if c.Has(k) { // a concurrent install put the same package there first
			return nil
		}
		return err
	}
	return nil
}

// SourceFile is written into every cached source checkout and into the copies
// in a project's vendor directory; it says what the directory holds.
const SourceFile = ".kiln-source.json"

// SourceInfo describes a source checkout.
type SourceInfo struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Commit     string `json:"commit"`
}

func sourceCheck(name, version string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid package name %q", name)
	}
	if !versionRe.MatchString(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	return nil
}

// SourceDir is where the source of a package version lives (whether or not it is
// there): sources/vendor/name/version. One checkout serves every build of the version.
func (c Cache) SourceDir(name, version string) string {
	return filepath.Join(c.Root, "sources", filepath.FromSlash(name), version)
}

// Source returns the record of the cached source of a package version.
func (c Cache) Source(name, version string) (SourceInfo, error) {
	if err := sourceCheck(name, version); err != nil {
		return SourceInfo{}, err
	}
	return ReadSourceInfo(c.SourceDir(name, version))
}

// ReadSourceInfo reads the record in a source directory.
func ReadSourceInfo(dir string) (SourceInfo, error) {
	raw, err := os.ReadFile(filepath.Join(dir, SourceFile))
	if err != nil {
		return SourceInfo{}, err
	}
	var si SourceInfo
	return si, json.Unmarshal(raw, &si)
}

// NewSourceTemp makes an empty directory in the cache to fetch a source into.
// Hand it to PutSource when it is complete, or remove it.
func (c Cache) NewSourceTemp() (string, error) {
	if err := os.MkdirAll(c.Root, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(c.Root, ".src-")
}

// PutSource records si in the finished checkout in dir and moves it into place,
// replacing an older checkout of the same version.
func (c Cache) PutSource(dir string, si SourceInfo) error {
	if err := sourceCheck(si.Name, si.Version); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(si, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, SourceFile), append(meta, '\n'), 0o644); err != nil {
		return err
	}
	final := c.SourceDir(si.Name, si.Version)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(dir, final); err != nil {
		if _, e := c.Source(si.Name, si.Version); e == nil { // a concurrent install got there first
			return nil
		}
		return err
	}
	return nil
}

// Package lock reads and writes kiln.lock, the record of exactly which
// versions of which packages a project uses.
//
// The lock pins versions and nothing about the machine: no operating system,
// compiler, build settings, binary identities or file hashes. Those differ
// between developers on different platforms and would make every update by
// one of them rewrite the file for the others. What the lock holds is the same
// everywhere: package, version, the commit the registry pinned for that
// version, and who requires whom. Platform-specific dependencies are included
// for every platform, so a lock written on Linux also pins the Windows-only
// packages. Each machine works out its own binaries for the locked versions.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
)

// Version is the lockfile format written by this code.
const Version = 2

// ErrOldFormat means the lockfile was written by an older kiln.
var ErrOldFormat = errors.New("the lockfile uses an older format (lock-version 1), which also stored machine-specific data")

type Package struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Commit   string   `json:"commit,omitempty"`
	Requires []string `json:"requires,omitempty"`
}

type File struct {
	LockVersion int       `json:"lock-version"`
	ContentHash string    `json:"content-hash"`
	Packages    []Package `json:"packages"`
}

// Read loads and validates a lockfile. It returns nil, nil if there is none.
func Read(path string, v *manifest.Validator) (*File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var probe struct {
		LockVersion int `json:"lock-version"`
	}
	if json.Unmarshal(raw, &probe) == nil && probe.LockVersion == 1 {
		return nil, ErrOldFormat
	}
	if err := v.ValidateLock(raw); err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Write validates and writes the lockfile atomically.
func (f *File) Write(path string, v *manifest.Validator) error {
	if f.Packages == nil {
		f.Packages = []Package{} // never null: a project may have no dependencies
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := v.ValidateLock(out); err != nil {
		return fmt.Errorf("internal error: the generated lockfile is invalid: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns the locked package with this name.
func (f *File) Get(name string) (Package, bool) {
	for _, p := range f.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return Package{}, false
}

// Versions maps package names to their locked versions.
func (f *File) Versions() (map[string]semver.Version, error) {
	out := make(map[string]semver.Version, len(f.Packages))
	for _, p := range f.Packages {
		v, err := semver.Parse(p.Version)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
		out[p.Name] = v
	}
	return out, nil
}

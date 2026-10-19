// Package project reads the manifest of the project being worked on and
// writes its lockfile.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/pixelpolishers/kiln-core/constraint"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/options"
	"github.com/pixelpolishers/kiln-core/solver"
)

const (
	ManifestFile = "kiln.json"
	LockFile     = "kiln.lock"
)

type Project struct {
	Dir      string
	Manifest manifest.Package
	Raw      []byte // the manifest as written
}

// Load reads and validates dir/kiln.json.
func Load(dir string, v *manifest.Validator) (Project, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if os.IsNotExist(err) {
		return Project{}, fmt.Errorf("no %s in %s (run kiln init, or cd into your project)", ManifestFile, dir)
	}
	if err != nil {
		return Project{}, err
	}
	if _, _, err := v.Parse(raw); err != nil {
		return Project{}, fmt.Errorf("%s is invalid: %w", ManifestFile, err)
	}
	m, err := manifest.ParsePackage(raw)
	if err != nil {
		return Project{}, err
	}
	return Project{Dir: dir, Manifest: m, Raw: raw}, nil
}

// Roots converts a require section into solver roots, leaving out optional
// dependencies and those restricted to other platforms. An empty osName keeps
// the platform-restricted ones too (what the lockfile needs).
func Roots(require map[string]manifest.Dependency, osName string) ([]solver.Dep, error) {
	names := make([]string, 0, len(require))
	for n := range require {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []solver.Dep
	for _, n := range names {
		d := require[n]
		if d.Optional || (osName != "" && len(d.Platforms) > 0 && !slices.Contains(d.Platforms, osName)) {
			continue
		}
		c, err := constraint.Parse(d.Version)
		if err != nil {
			return nil, fmt.Errorf("require %s: %w", n, err)
		}
		out = append(out, solver.Dep{Name: n, Constraint: c})
	}
	return out, nil
}

// OptionRequests lists the options the project's own require section sets on
// its dependencies. They count as root requests and override dependents.
func OptionRequests(require map[string]manifest.Dependency) []options.Request {
	var out []options.Request
	for dep, d := range require {
		for opt, val := range d.Options {
			out = append(out, options.Request{Package: dep, Option: opt, Value: val, From: "root", Root: true})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Package+"\x00"+out[i].Option < out[j].Package+"\x00"+out[j].Option
	})
	return out
}

// ContentHash identifies the dependency declarations of a manifest, so a
// lockfile can tell when it is stale.
func ContentHash(raw []byte) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"require": doc["require"], "require-dev": doc["require-dev"]})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

package install

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/pixelpolishers/kiln-cli/internal/lock"
	"github.com/pixelpolishers/kiln-cli/internal/project"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
)

// OutdatedPackage compares one locked package with the registry.
type OutdatedPackage struct {
	Name   string
	Direct bool // required by kiln.json itself, not only by another package
	Locked string
	// Wanted is the version `kiln update` would lock: the newest one the
	// constraints of the project and of all packages allow.
	Wanted string
	// Latest is the newest stable version in the registry, whatever the constraints say.
	Latest string
}

// Outdated reports whether a newer version exists.
func (o OutdatedPackage) Outdated() bool { return o.Wanted != o.Locked || o.Latest != o.Locked }

// HeldBack reports that the newest version exists but the constraints keep it out.
func (o OutdatedPackage) HeldBack() bool { return o.Latest != o.Wanted }

// OutdatedResult is the outcome of Outdated.
type OutdatedResult struct {
	Packages []OutdatedPackage // every locked package, by name
	// Stale is true when kiln.json changed since kiln.lock was written; the
	// comparison is then against a lock that does not match the project.
	Stale bool
}

// Outdated compares the packages in kiln.lock with the registry. It changes nothing.
func Outdated(ctx context.Context, env Env, dir string) (OutdatedResult, error) {
	proj, err := project.Load(dir, env.Validator)
	if err != nil {
		return OutdatedResult{}, err
	}
	lockPath := filepath.Join(dir, project.LockFile)
	lk, err := lock.Read(lockPath, env.Validator)
	if err != nil {
		return OutdatedResult{}, &InvalidLockError{Path: lockPath, Err: err}
	}
	if lk == nil {
		return OutdatedResult{}, &NoLockError{}
	}
	hash, err := project.ContentHash(proj.Raw)
	if err != nil {
		return OutdatedResult{}, err
	}

	require := map[string]manifest.Dependency{}
	for n, d := range proj.Manifest.RequireDev {
		require[n] = d
	}
	for n, d := range proj.Manifest.Require {
		require[n] = d
	}
	// what `kiln update` would lock
	next, err := env.resolveLock(ctx, require, hash, nil, nil)
	if err != nil {
		return OutdatedResult{}, err
	}

	res := OutdatedResult{Stale: lk.ContentHash != hash}
	for _, p := range lk.Packages {
		wanted := p.Version
		if np, ok := next.Get(p.Name); ok {
			wanted = np.Version
		}
		latest, err := env.newestStable(ctx, p.Name)
		if err != nil {
			return OutdatedResult{}, err
		}
		if latest == "" {
			latest = p.Version
		}
		_, direct := require[p.Name]
		res.Packages = append(res.Packages, OutdatedPackage{Name: p.Name, Direct: direct, Locked: p.Version, Wanted: newer(p.Version, wanted), Latest: newer(p.Version, latest)})
	}
	sort.Slice(res.Packages, func(i, j int) bool { return res.Packages[i].Name < res.Packages[j].Name })
	return res, nil
}

// newestStable is the highest version of name that is neither a prerelease nor flagged.
func (e Env) newestStable(ctx context.Context, name string) (string, error) {
	versions, err := e.Source.Versions(ctx, name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var best semver.Version
	found := false
	for _, v := range versions {
		if v.IsPrerelease() {
			continue
		}
		if !found || semver.Compare(v, best) > 0 {
			best, found = v, true
		}
	}
	if !found {
		return "", nil
	}
	return best.String(), nil
}

// newer returns candidate if it is a higher version than current, else current:
// a locked prerelease or an older registry state is never reported as "downgrade".
func newer(current, candidate string) string {
	c, err1 := semver.Parse(current)
	n, err2 := semver.Parse(candidate)
	if err1 != nil || err2 != nil || semver.Compare(n, c) <= 0 {
		return current
	}
	return candidate
}

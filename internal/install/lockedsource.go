package install

import (
	"context"
	"fmt"
	"slices"

	"github.com/pixelpolishers/kiln-cli/internal/lock"
	"github.com/pixelpolishers/kiln-core/semver"
	"github.com/pixelpolishers/kiln-core/solver"
)

// NotLockedError means a package is needed that the lockfile does not contain
// (a new dependency, or one that applies only to another platform).
type NotLockedError struct{ Package string }

func (e *NotLockedError) Error() string {
	return fmt.Sprintf("%s is required but is not in kiln.lock", e.Package)
}

// LockedUnavailableError means the registry no longer offers a locked version
// (removed, or flagged because its tag moved).
type LockedUnavailableError struct {
	Package string
	Version semver.Version
}

func (e *LockedUnavailableError) Error() string {
	return fmt.Sprintf("kiln.lock pins %s %s, which the registry no longer offers (removed or flagged)", e.Package, e.Version)
}

// lockedSource offers the solver only the locked version of a locked package,
// so the solver reproduces the lock instead of choosing newer versions.
type lockedSource struct {
	inner  solver.Source
	locked map[string]semver.Version
	// free packages may change (kiln update <pkg>).
	free map[string]bool
	// strict refuses packages that are not in the lock (kiln install). When
	// false (kiln update <pkg>) new packages are resolved normally.
	strict bool
}

func newLockedSource(inner solver.Source, lk *lock.File, free []string, strict bool) (*lockedSource, error) {
	vs, err := lk.Versions()
	if err != nil {
		return nil, err
	}
	f := map[string]bool{}
	for _, n := range free {
		f[n] = true
	}
	return &lockedSource{inner: inner, locked: vs, free: f, strict: strict}, nil
}

func (s *lockedSource) Versions(ctx context.Context, name string) ([]semver.Version, error) {
	if s.free[name] {
		return s.inner.Versions(ctx, name)
	}
	if v, ok := s.locked[name]; ok {
		available, err := s.inner.Versions(ctx, name)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(available, func(a semver.Version) bool { return semver.Compare(a, v) == 0 }) {
			return nil, &LockedUnavailableError{Package: name, Version: v}
		}
		return []semver.Version{v}, nil
	}
	if s.strict {
		return nil, &NotLockedError{Package: name}
	}
	return s.inner.Versions(ctx, name)
}

func (s *lockedSource) Dependencies(ctx context.Context, name string, v semver.Version) ([]solver.Dep, error) {
	return s.inner.Dependencies(ctx, name, v)
}

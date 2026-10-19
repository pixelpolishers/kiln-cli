// Package registrysrc adapts the registry client to the solver's Source interface.
package registrysrc

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/pixelpolishers/kiln-cli/internal/client"
	"github.com/pixelpolishers/kiln-core/api"
	"github.com/pixelpolishers/kiln-core/constraint"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
	"github.com/pixelpolishers/kiln-core/solver"
)

// Source answers the solver's questions from a registry.
type Source struct {
	Client *client.Client
	// OS is the target operating system; dependencies restricted to other
	// platforms are left out.
	OS string
	// AllPlatforms ignores the platform restrictions of dependencies, so the
	// result is what is needed on every platform (used to compute the lock).
	AllPlatforms bool

	details map[string]api.VersionDetail // fetched versions, by name@version
}

// WithAllPlatforms returns a view of the same registry that includes
// dependencies restricted to other platforms. It shares the fetch cache.
func (s *Source) WithAllPlatforms() *Source {
	if s.details == nil {
		s.details = map[string]api.VersionDetail{}
	}
	c := *s
	c.AllPlatforms = true
	return &c
}

func (s *Source) detail(ctx context.Context, name string, v semver.Version) (api.VersionDetail, error) {
	key := name + "@" + v.String()
	if d, ok := s.details[key]; ok {
		return d, nil
	}
	d, err := s.Client.Version(ctx, name, v.String())
	if err != nil {
		return api.VersionDetail{}, err
	}
	if s.details == nil {
		s.details = map[string]api.VersionDetail{}
	}
	s.details[key] = d
	return d, nil
}

// Manifest returns the manifest of name@v and the commit it was published for.
func (s *Source) Manifest(ctx context.Context, name string, v semver.Version) (manifest.Package, string, error) {
	d, err := s.detail(ctx, name, v)
	if err != nil {
		return manifest.Package{}, "", err
	}
	m, err := manifest.ParsePackage(d.Manifest)
	if err != nil {
		return manifest.Package{}, "", fmt.Errorf("%s@%s: unreadable manifest: %w", name, v, err)
	}
	return m, d.Commit, nil
}

// Versions lists the versions that may be installed: flagged ones (whose tag
// moved after publishing) are excluded.
func (s *Source) Versions(ctx context.Context, name string) ([]semver.Version, error) {
	pkg, err := s.Client.Package(ctx, name)
	if client.IsNotFound(err) {
		return nil, fmt.Errorf("%s: %w", name, solver.ErrUnknownPackage)
	}
	if err != nil {
		return nil, err
	}
	var out []semver.Version
	for _, v := range pkg.Versions {
		if v.Flagged {
			continue
		}
		p, err := semver.Parse(v.Version)
		if err != nil {
			continue // the registry should never serve these; skip rather than fail
		}
		out = append(out, p)
	}
	return out, nil
}

// Dependencies reads the manifest of name@v and returns its required packages.
// Optional dependencies are left out, as are those that do not apply to s.OS.
func (s *Source) Dependencies(ctx context.Context, name string, v semver.Version) ([]solver.Dep, error) {
	m, _, err := s.Manifest(ctx, name, v)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m.Require))
	for n := range m.Require {
		names = append(names, n)
	}
	sort.Strings(names)

	var deps []solver.Dep
	for _, n := range names {
		d := m.Require[n]
		if d.Optional || (!s.AllPlatforms && len(d.Platforms) > 0 && !slices.Contains(d.Platforms, s.OS)) {
			continue
		}
		c, err := constraint.Parse(d.Version)
		if err != nil {
			return nil, fmt.Errorf("%s@%s requires %s: %w", name, v, n, err)
		}
		deps = append(deps, solver.Dep{Name: n, Constraint: c})
	}
	return deps, nil
}

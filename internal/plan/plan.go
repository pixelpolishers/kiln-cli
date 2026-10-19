// Package plan turns a solved dependency graph into a build plan: the resolved
// options of every package and the asset id of the binary that is needed.
package plan

import (
	"context"
	"fmt"

	"github.com/pixelpolishers/kiln-core/assetid"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/options"
	"github.com/pixelpolishers/kiln-core/semver"
	"github.com/pixelpolishers/kiln-core/solver"
)

// ManifestSource provides the manifest of an exact package version.
type ManifestSource interface {
	Manifest(ctx context.Context, name string, v semver.Version) (manifest.Package, string, error)
}

// Package is one entry of the plan.
type Package struct {
	Name     string
	Version  semver.Version
	Commit   string
	Manifest manifest.Package
	Options  map[string]any
	Kind     assetid.Kind
	// Settings is the part of the build settings that affects this package.
	Settings map[string]any
	Requires []string
	ID       string
}

// OptionsError means the requested option values cannot be satisfied.
type OptionsError struct{ Err error }

func (e *OptionsError) Error() string { return e.Err.Error() }
func (e *OptionsError) Unwrap() error { return e.Err }

// Plan lists the packages with dependencies first.
type Plan struct {
	Packages []Package
}

func (p Plan) Get(name string) (Package, bool) {
	for _, pkg := range p.Packages {
		if pkg.Name == name {
			return pkg, true
		}
	}
	return Package{}, false
}

// Input is what Build needs besides the solution.
type Input struct {
	Solution solver.Solution
	// Settings is the full build settings object (see settings.schema.json).
	Settings map[string]any
	// Requests are options set by the root project (its manifest and -o flags).
	Requests []options.Request
}

// KindOf classifies a package for asset id purposes.
func KindOf(m manifest.Package) assetid.Kind {
	k := assetid.Kind{Language: m.Language}
	if k.Language == "" {
		k.Language = "c++"
	}
	k.HeaderOnly = len(m.Targets) > 0
	for _, t := range m.Targets {
		if t.Type != "header-only" {
			k.HeaderOnly = false
		}
	}
	return k
}

// Build resolves options and computes asset ids, dependencies first.
func Build(ctx context.Context, src ManifestSource, in Input) (Plan, error) {
	pkgs := make([]Package, len(in.Solution.Packages))
	specs := map[string]map[string]manifest.Option{}
	for i, sp := range in.Solution.Packages {
		m, commit, err := src.Manifest(ctx, sp.Name, sp.Version)
		if err != nil {
			return Plan{}, err
		}
		pkgs[i] = Package{Name: sp.Name, Version: sp.Version, Commit: commit, Manifest: m, Kind: KindOf(m), Requires: sp.Requires}
		specs[sp.Name] = m.Options
	}

	reqs := append([]options.Request(nil), in.Requests...)
	for _, p := range pkgs {
		from := p.Name + "@" + p.Version.String()
		for dep, d := range p.Manifest.Require {
			for opt, val := range d.Options {
				reqs = append(reqs, options.Request{Package: dep, Option: opt, Value: val, From: from})
			}
		}
	}
	resolved, err := options.Resolve(specs, reqs)
	if err != nil {
		return Plan{}, &OptionsError{err}
	}

	ids := map[string]string{}
	versions := map[string]string{}
	for i := range pkgs {
		p := &pkgs[i]
		p.Options = resolved[p.Name]
		p.Settings = assetid.Relevant(in.Settings, p.Kind)
		deps := map[string]assetid.Dep{}
		for _, r := range p.Requires {
			deps[r] = assetid.Dep{Version: versions[r], ID: ids[r]}
		}
		id, err := assetid.Compute(assetid.Input{
			Package: p.Name, Version: p.Version.String(),
			Settings: p.Settings, Options: p.Options, Deps: deps,
		})
		if err != nil {
			return Plan{}, fmt.Errorf("%s: %w", p.Name, err)
		}
		p.ID = id
		ids[p.Name], versions[p.Name] = id, p.Version.String()
	}
	return Plan{Packages: pkgs}, nil
}

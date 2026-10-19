package install

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/pixelpolishers/kiln-cli/internal/lock"
	"github.com/pixelpolishers/kiln-cli/internal/project"
	"github.com/pixelpolishers/kiln-core/constraint"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
	"github.com/pixelpolishers/kiln-core/solver"
)

// NotInstalledError means the package is not in kiln.lock.
type NotInstalledError struct{ Package string }

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("%s is not in %s: nothing requires it", e.Package, project.LockFile)
}

// Step is one package on a path from kiln.json to a package.
type Step struct {
	Name    string
	Version string
	// Constraint is what the previous step (or kiln.json) asks of this package.
	Constraint string
}

// WhyResult explains why a package is installed.
type WhyResult struct {
	Name, Version string
	// Direct is true when kiln.json itself requires the package.
	Direct           bool
	DirectConstraint string
	// Paths lead from kiln.json to the package, one per way it is needed. A
	// direct requirement is the path of one step.
	Paths [][]Step
	Stale bool
}

// maxPaths bounds the output for large graphs.
const maxPaths = 50

// graph is the locked dependency graph with the constraint on every edge.
type graph struct {
	versions map[string]string              // name -> locked version
	edges    map[string][]edge              // name -> what it requires
	roots    []edge                         // what kiln.json requires
	manifest map[string]manifest.Package    // name -> manifest of the locked version
	require  map[string]manifest.Dependency // kiln.json
	lock     *lock.File
	stale    bool
}

type edge struct{ to, constraint string }

// loadGraph reads kiln.json, kiln.lock and the manifests of the locked versions.
func (e Env) loadGraph(ctx context.Context, dir string) (*graph, error) {
	proj, err := project.Load(dir, e.Validator)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, project.LockFile)
	lk, err := lock.Read(lockPath, e.Validator)
	if err != nil {
		return nil, &InvalidLockError{Path: lockPath, Err: err}
	}
	if lk == nil {
		return nil, &NoLockError{}
	}
	hash, err := project.ContentHash(proj.Raw)
	if err != nil {
		return nil, err
	}
	g := &graph{
		versions: map[string]string{}, edges: map[string][]edge{}, manifest: map[string]manifest.Package{},
		require: map[string]manifest.Dependency{}, lock: lk, stale: lk.ContentHash != hash,
	}
	for n, d := range proj.Manifest.RequireDev {
		g.require[n] = d
	}
	for n, d := range proj.Manifest.Require {
		g.require[n] = d
	}
	for _, p := range lk.Packages {
		g.versions[p.Name] = p.Version
		v, err := semver.Parse(p.Version)
		if err != nil {
			return nil, &InvalidLockError{Path: lockPath, Err: fmt.Errorf("%s: %w", p.Name, err)}
		}
		m, _, err := e.Source.Manifest(ctx, p.Name, v)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", p.Name, p.Version, err)
		}
		g.manifest[p.Name] = m
		for _, r := range p.Requires {
			g.edges[p.Name] = append(g.edges[p.Name], edge{r, m.Require[r].Version})
		}
	}
	names := make([]string, 0, len(g.require))
	for n := range g.require {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := g.versions[n]; ok {
			g.roots = append(g.roots, edge{n, g.require[n].Version})
		}
	}
	return g, nil
}

// Why explains why name is installed, from kiln.lock.
func Why(ctx context.Context, env Env, dir, name string) (WhyResult, error) {
	g, err := env.loadGraph(ctx, dir)
	if err != nil {
		return WhyResult{}, err
	}
	ver, ok := g.versions[name]
	if !ok {
		return WhyResult{}, &NotInstalledError{Package: name}
	}
	res := WhyResult{Name: name, Version: ver, Stale: g.stale}
	if d, ok := g.require[name]; ok {
		res.Direct, res.DirectConstraint = true, d.Version
	}

	var walk func(path []Step, at string)
	walk = func(path []Step, at string) {
		if len(res.Paths) >= maxPaths {
			return
		}
		if at == name {
			res.Paths = append(res.Paths, append([]Step(nil), path...))
			return
		}
		for _, e := range g.edges[at] {
			if inPath(path, e.to) {
				continue
			}
			walk(append(path, Step{Name: e.to, Version: g.versions[e.to], Constraint: e.constraint}), e.to)
		}
	}
	for _, r := range g.roots {
		walk([]Step{{Name: r.to, Version: g.versions[r.to], Constraint: r.constraint}}, r.to)
	}
	// shortest explanations first, then alphabetical
	sort.SliceStable(res.Paths, func(i, j int) bool {
		if len(res.Paths[i]) != len(res.Paths[j]) {
			return len(res.Paths[i]) < len(res.Paths[j])
		}
		return res.Paths[i][0].Name < res.Paths[j][0].Name
	})
	return res, nil
}

func inPath(path []Step, name string) bool {
	for _, s := range path {
		if s.Name == name {
			return true
		}
	}
	return false
}

// Blocker is a requirement that keeps a package version out.
type Blocker struct {
	// By is the package that holds it back; empty means kiln.json itself.
	By        string
	ByVersion string
	// Requires is what it asks for, for example "~11.0.0".
	Requires string
	// Allowing is the newest version of By whose requirement admits the wanted
	// version, when there is one.
	Allowing string
}

// WhyNotResult explains whether a package version can be installed.
type WhyNotResult struct {
	Name, Spec string
	Installed  string   // locked version, empty if not installed
	Available  []string // all installable versions, newest first
	Candidates []string // those that match Spec, newest first
	Blockers   []Blocker
	// SolveErr is why no set of versions works at all (nil when one does).
	SolveErr error
	// Changes lists what the working set of versions would change compared to
	// the lock: "name: old -> new" or "name: new".
	Changes []string
	Stale   bool
}

// WhyNot explains what prevents name (restricted to spec, "" for any) from
// being installed together with the project's requirements. Nothing is changed.
func WhyNot(ctx context.Context, env Env, dir, name, spec string) (WhyNotResult, error) {
	if spec == "" {
		spec = "*"
	}
	want, err := constraint.Parse(spec)
	if err != nil {
		return WhyNotResult{}, fmt.Errorf("%s: %w", spec, err)
	}
	g, err := env.loadGraph(ctx, dir)
	if err != nil {
		return WhyNotResult{}, err
	}
	res := WhyNotResult{Name: name, Spec: spec, Installed: g.versions[name], Stale: g.stale}

	versions, err := env.Source.Versions(ctx, name)
	if err != nil {
		return res, err
	}
	sort.Slice(versions, func(i, j int) bool { return semver.Compare(versions[i], versions[j]) > 0 })
	for _, v := range versions {
		if !v.IsPrerelease() || want.Matches(v) {
			res.Available = append(res.Available, v.String())
		}
		if want.Matches(v) {
			res.Candidates = append(res.Candidates, v.String())
		}
	}
	if len(res.Candidates) == 0 {
		return res, nil
	}
	var candidates []semver.Version
	for _, c := range res.Candidates {
		v, _ := semver.Parse(c)
		candidates = append(candidates, v)
	}
	admits := func(spec string) bool {
		if spec == "" {
			return true
		}
		c, err := constraint.Parse(spec)
		if err != nil {
			return true // cannot tell; do not blame it
		}
		for _, v := range candidates {
			if c.Matches(v) {
				return true
			}
		}
		return false
	}

	// kiln.json itself
	if d, ok := g.require[name]; ok && !admits(d.Version) {
		res.Blockers = append(res.Blockers, Blocker{Requires: d.Version})
	}
	// the locked packages that require name
	by := make([]string, 0, len(g.edges))
	for n := range g.edges {
		by = append(by, n)
	}
	sort.Strings(by)
	for _, n := range by {
		for _, e := range g.edges[n] {
			if e.to != name || admits(e.constraint) {
				continue
			}
			b := Blocker{By: n, ByVersion: g.versions[n], Requires: e.constraint}
			b.Allowing = env.versionAllowing(ctx, g, n, name, admits)
			res.Blockers = append(res.Blockers, b)
		}
	}

	// can any set of versions work at all?
	roots, err := project.Roots(g.require, "")
	if err != nil {
		return res, err
	}
	roots = append(roots, solver.Dep{Name: name, Constraint: want})
	sol, err := solver.Solve(ctx, env.Source.WithAllPlatforms(), roots, solver.Options{})
	if err != nil {
		var conflict *solver.ConflictError
		if errors.As(err, &conflict) {
			res.SolveErr = err
			return res, nil
		}
		return res, err
	}
	for _, p := range sol.Packages {
		old, had := g.versions[p.Name]
		switch {
		case !had:
			res.Changes = append(res.Changes, fmt.Sprintf("%s: %s (new)", p.Name, p.Version))
		case old != p.Version.String():
			res.Changes = append(res.Changes, fmt.Sprintf("%s: %s -> %s", p.Name, old, p.Version))
		}
	}
	sort.Strings(res.Changes)
	return res, nil
}

// versionAllowing returns the newest version of pkg, newer than the locked one,
// that kiln.json would accept and whose requirement on dep admits the wanted
// versions. At most 25 versions are examined.
func (e Env) versionAllowing(ctx context.Context, g *graph, pkg, dep string, admits func(string) bool) string {
	versions, err := e.Source.Versions(ctx, pkg)
	if err != nil {
		return ""
	}
	sort.Slice(versions, func(i, j int) bool { return semver.Compare(versions[i], versions[j]) > 0 })
	var rootOK constraint.Constraint
	haveRoot := false
	if d, ok := g.require[pkg]; ok {
		if c, err := constraint.Parse(d.Version); err == nil {
			rootOK, haveRoot = c, true
		}
	}
	locked, _ := semver.Parse(g.versions[pkg])
	checked := 0
	for _, v := range versions {
		if semver.Compare(v, locked) <= 0 {
			break // only upgrades help
		}
		if v.IsPrerelease() || (haveRoot && !rootOK.Matches(v)) {
			continue
		}
		if checked++; checked > 25 {
			break
		}
		m, _, err := e.Source.Manifest(ctx, pkg, v)
		if err != nil {
			continue
		}
		dd, requires := m.Require[dep]
		if !requires || admits(dd.Version) {
			return v.String()
		}
	}
	return ""
}

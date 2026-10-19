// Package install implements `kiln install`: solve versions, plan binaries,
// obtain them (cache, local repository, or a build from source), and generate
// the build integration and lockfile.
package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/archive"
	"github.com/pixelpolishers/kiln-cli/internal/build"
	"github.com/pixelpolishers/kiln-cli/internal/cache"
	"github.com/pixelpolishers/kiln-cli/internal/integration"
	"github.com/pixelpolishers/kiln-cli/internal/localrepo"
	"github.com/pixelpolishers/kiln-cli/internal/lock"
	"github.com/pixelpolishers/kiln-cli/internal/pack"
	"github.com/pixelpolishers/kiln-cli/internal/plan"
	"github.com/pixelpolishers/kiln-cli/internal/project"
	"github.com/pixelpolishers/kiln-cli/internal/registrysrc"
	"github.com/pixelpolishers/kiln-cli/internal/settings"
	"github.com/pixelpolishers/kiln-cli/internal/source"
	"github.com/pixelpolishers/kiln-cli/internal/sysreq"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/options"
	"github.com/pixelpolishers/kiln-core/solver"
	"github.com/pixelpolishers/kiln-core/when"
)

// Policy says where binaries may come from.
type Policy struct {
	// BinaryOnly never builds from source.
	BinaryOnly bool
	// SourceOnly never uses a prebuilt binary (cache entries are still reused).
	SourceOnly bool
}

// Env holds the services install works with.
type Env struct {
	Source    *registrysrc.Source
	Local     *localrepo.Repo // optional
	Cache     cache.Cache
	Validator *manifest.Validator
	Progress  io.Writer // optional

	Policy Policy
	// Fetch retrieves source for builds. Nil disables source builds.
	Fetch source.Fetcher
	// Toolchain is the compiler used for source builds.
	Toolchain build.Toolchain
	// Detected, if set, is what this machine really has. A source build is
	// refused when the requested settings describe a different toolchain,
	// because the result would not be the binary its id claims.
	Detected *settings.Settings
	Jobs     int
	// BuildOutput, if set, receives the output of builds as they run.
	BuildOutput io.Writer
	// SysReq answers "does this machine have X" for build requirements.
	// Nil means the real machine.
	SysReq *sysreq.Env
}

type Config struct {
	Dir      string
	Settings settings.Settings
	// Requests are -o overrides from the command line.
	Requests []options.Request
	// CheckOnly stops after the system requirements check: nothing is built or written.
	CheckOnly bool
	// Mode is ModeInstall (follow kiln.lock) or ModeUpdate (resolve again and rewrite it).
	Mode Mode
	// UpdatePackages limits ModeUpdate to these packages; the others stay at
	// their locked versions. Empty means everything.
	UpdatePackages []string
	// RequireLock makes ModeInstall fail instead of creating a lockfile.
	RequireLock bool
	// NoSources skips copying the package sources into vendor/kiln.
	NoSources bool
	// KeepLocked, with ModeUpdate, keeps every locked package at its locked
	// version except UpdatePackages (which may be absent from the lock, and
	// may be empty). Packages new to the lock are resolved normally. This is
	// what `require` and `remove` use.
	KeepLocked bool
}

// Mode says how the lockfile is used.
type Mode int

const (
	// ModeInstall installs exactly what kiln.lock says and never changes it.
	// Without a lockfile it resolves, installs and creates one.
	ModeInstall Mode = iota
	// ModeUpdate resolves again and rewrites kiln.lock.
	ModeUpdate
)

type Result struct {
	// ToBuild lists the packages that need a source build.
	ToBuild   []string
	Plan      plan.Plan
	Origins   map[string]string // package -> how its binary was obtained
	Warnings  []string
	CMakeFile string
	// SourcesDir is where the package sources were copied; empty when not done.
	SourcesDir string
	LockFile   string
	// LockUsed is true when the install followed an existing kiln.lock.
	LockUsed bool
	// LockWritten is true when kiln.lock was created or rewritten.
	LockWritten bool
	// LockCreated is true when there was no kiln.lock before.
	LockCreated bool
	// Notes are remarks that are not warnings (for example differing settings).
	Notes []string
}

// NoBinaryError means no binary exists for a package and none could be built.
type NoBinaryError struct {
	Package plan.Package
	Tried   []string
	// SourceDisabled is true when building from source was not allowed.
	SourceDisabled bool
}

func (e *NoBinaryError) Error() string {
	return fmt.Sprintf("no binary for %s %s with id %s (tried: %s)", e.Package.Name, e.Package.Version, e.Package.ID, strings.Join(e.Tried, ", "))
}

// StaleLockError means kiln.json's dependencies changed since kiln.lock was written.
type StaleLockError struct{}

func (e *StaleLockError) Error() string {
	return "kiln.lock is out of date: the dependencies in kiln.json changed since it was written"
}

// NoLockError means a lockfile was required but does not exist.
type NoLockError struct{}

func (e *NoLockError) Error() string { return "there is no kiln.lock" }

// InvalidLockError means kiln.lock cannot be read.
type InvalidLockError struct {
	Path string
	Err  error
}

func (e *InvalidLockError) Error() string { return fmt.Sprintf("%s is not valid: %v", e.Path, e.Err) }
func (e *InvalidLockError) Unwrap() error { return e.Err }

// CommitChangedError means the registry reports a different commit for a
// version than kiln.lock pinned: the tag was moved or the version republished.
type CommitChangedError struct {
	Package, Version, Locked, Now string
}

func (e *CommitChangedError) Error() string {
	return fmt.Sprintf("%s %s: kiln.lock pins commit %s but the registry now says %s (the tag was moved or the version republished)", e.Package, e.Version, short(e.Locked), short(e.Now))
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// MissingRequirementsError means the machine lacks something a source build
// needs. All missing requirements of all packages to build are collected.
type MissingRequirementsError struct {
	Failures []sysreq.Failure
}

func (e *MissingRequirementsError) Error() string {
	return "missing system requirements:\n" + sysreq.Format(e.Failures)
}

// SourceBuildError means building from source failed.
type SourceBuildError struct {
	Package plan.Package
	Err     error
}

func (e *SourceBuildError) Error() string {
	return fmt.Sprintf("building %s %s from source failed: %v", e.Package.Name, e.Package.Version, e.Err)
}
func (e *SourceBuildError) Unwrap() error { return e.Err }

// VerifyError means a binary was found but failed verification.
type VerifyError struct {
	Package string
	Reason  string
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("%s: verification failed: %s", e.Package, e.Reason)
}

func (e Env) progress(format string, args ...any) {
	if e.Progress != nil {
		fmt.Fprintf(e.Progress, format+"\n", args...)
	}
}

// Run performs the installation.
func Run(ctx context.Context, env Env, cfg Config) (Result, error) {
	proj, err := project.Load(cfg.Dir, env.Validator)
	if err != nil {
		return Result{}, err
	}
	smap, err := cfg.Settings.Map()
	if err != nil {
		return Result{}, err
	}

	require := map[string]manifest.Dependency{}
	for n, d := range proj.Manifest.RequireDev {
		require[n] = d
	}
	for n, d := range proj.Manifest.Require {
		require[n] = d
	}
	roots, err := project.Roots(require, cfg.Settings.OS) // what this platform needs
	if err != nil {
		return Result{}, err
	}
	if len(roots) == 0 {
		env.progress("nothing to install: %s requires no packages", project.ManifestFile)
	}

	hash, err := project.ContentHash(proj.Raw)
	if err != nil {
		return Result{}, err
	}
	lockPath := filepath.Join(cfg.Dir, project.LockFile)
	lk, err := lock.Read(lockPath, env.Validator)
	if err != nil {
		// A plain update replaces the whole lock, so a broken old one does not matter:
		// it is the way out of this error.
		if cfg.Mode == ModeUpdate && len(cfg.UpdatePackages) == 0 {
			lk = nil
		} else {
			return Result{}, &InvalidLockError{Path: lockPath, Err: err}
		}
	}

	res := Result{Origins: map[string]string{}, LockFile: lockPath}

	// Step 1: which versions? Either follow the existing lock, or work out a new
	// one. A new lock covers every platform, so it is the same whoever writes it.
	var pinned *lock.File // the lock that decides the versions
	var fresh *lock.File  // a lock to write once the install succeeded
	switch cfg.Mode {
	case ModeInstall:
		switch {
		case lk != nil:
			if lk.ContentHash != hash {
				return res, &StaleLockError{}
			}
			pinned, res.LockUsed = lk, true
		case cfg.RequireLock:
			return res, &NoLockError{}
		default:
			if fresh, err = env.resolveLock(ctx, require, hash, nil, nil); err != nil {
				return res, err
			}
			pinned = fresh
		}
	case ModeUpdate:
		var base *lock.File
		var free []string
		if len(cfg.UpdatePackages) > 0 || cfg.KeepLocked {
			switch {
			case lk == nil && !cfg.KeepLocked:
				return res, fmt.Errorf("there is no %s to update from; run kiln install first, or kiln update without package names", project.LockFile)
			case lk != nil:
				for _, n := range cfg.UpdatePackages {
					if _, ok := lk.Get(n); !ok && !cfg.KeepLocked {
						return res, fmt.Errorf("%s is not in %s", n, project.LockFile)
					}
				}
				base, free = lk, cfg.UpdatePackages
			}
		}
		if fresh, err = env.resolveLock(ctx, require, hash, base, free); err != nil {
			return res, err
		}
		pinned = fresh
	}

	// Step 2: what does THIS machine need? Exactly the locked versions, limited
	// to the packages that apply to this platform.
	locked, err := newLockedSource(env.Source, pinned, nil, true)
	if err != nil {
		return res, &InvalidLockError{Path: lockPath, Err: err}
	}
	sol, err := solver.Solve(ctx, locked, roots, solver.Options{})
	if err != nil {
		return res, err
	}
	reqs := append(project.OptionRequests(require), cfg.Requests...)
	pl, err := plan.Build(ctx, env.Source, plan.Input{Solution: sol, Settings: smap, Requests: reqs})
	if err != nil {
		return res, err
	}
	res.Plan = pl
	if res.LockUsed {
		// the registry must still describe the locked versions by the same commits
		if err := checkCommits(pl, pinned); err != nil {
			return res, err
		}
	}

	toBuild, err := env.packagesToBuild(pl)
	if err != nil {
		return res, err
	}
	for _, p := range toBuild {
		res.ToBuild = append(res.ToBuild, p.Name)
	}
	required, optional := env.checkRequirements(pl.Packages, toBuild, cfg.Settings.OS, smap)
	for _, f := range optional {
		hint := ""
		if f.Hint != "" {
			hint = "; to fix: " + f.Hint
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s: optional %s is missing (%s)%s", f.Package, f.Version, f.Req.Name, f.Reason, hint))
	}
	if len(required) > 0 {
		return res, &MissingRequirementsError{Failures: required}
	}
	if cfg.CheckOnly {
		return res, nil
	}

	for _, p := range pl.Packages {
		origin, warn, err := env.obtain(ctx, pl, p, cfg.Settings, smap)
		if err != nil {
			return res, err
		}
		res.Origins[p.Name] = origin
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		env.progress("  %-24s %-10s %s", p.Name, p.Version, origin)
	}

	installed, err := env.load(pl)
	if err != nil {
		return res, err
	}
	cm, err := integration.CMake(installed)
	if err != nil {
		return res, err
	}
	vendorDir := filepath.Join(cfg.Dir, "vendor")
	outDir := filepath.Join(vendorDir, "kiln")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return res, err
	}
	res.CMakeFile = filepath.Join(outDir, "kiln.cmake")
	if err := os.WriteFile(res.CMakeFile, []byte(cm), 0o644); err != nil {
		return res, err
	}
	if !cfg.NoSources {
		res.Warnings = append(res.Warnings, env.vendorSources(ctx, vendorDir, pl)...)
		res.SourcesDir = vendorDir
	}

	if fresh != nil {
		if err := fresh.Write(lockPath, env.Validator); err != nil {
			return res, err
		}
		res.LockWritten, res.LockCreated = true, lk == nil
	}
	return res, nil
}

// resolveLock works out the versions of the whole dependency graph for every
// platform and returns them as a lock. With a base lock, the packages in base
// keep their locked versions except those named in free. Nothing in the result
// depends on the machine it is computed on.
func (e Env) resolveLock(ctx context.Context, require map[string]manifest.Dependency, hash string, base *lock.File, free []string) (*lock.File, error) {
	all := e.Source.WithAllPlatforms()
	roots, err := project.Roots(require, "") // every platform
	if err != nil {
		return nil, err
	}
	var src solver.Source = all
	if base != nil {
		ls, err := newLockedSource(all, base, free, false)
		if err != nil {
			return nil, &InvalidLockError{Path: project.LockFile, Err: err}
		}
		src = ls
	}
	sol, err := solver.Solve(ctx, src, roots, solver.Options{})
	if err != nil {
		return nil, err
	}

	skip := map[string]bool{}
	for _, n := range free {
		skip[n] = true
	}
	out := &lock.File{LockVersion: lock.Version, ContentHash: hash, Packages: []lock.Package{}}
	for _, p := range sol.Packages {
		_, commit, err := all.Manifest(ctx, p.Name, p.Version)
		if err != nil {
			return nil, err
		}
		lp := lock.Package{Name: p.Name, Version: p.Version.String(), Requires: p.Requires}
		if realCommit(commit) {
			lp.Commit = commit
		}
		if base != nil && !skip[p.Name] {
			if old, ok := base.Get(p.Name); ok && old.Version == lp.Version && old.Commit != "" && lp.Commit != "" && old.Commit != lp.Commit {
				return nil, &CommitChangedError{Package: p.Name, Version: lp.Version, Locked: old.Commit, Now: lp.Commit}
			}
		}
		out.Packages = append(out.Packages, lp)
	}
	return out, nil
}

// realCommit reports whether the registry pinned an actual commit (the demo
// data uses all zeros for "not pinned").
func realCommit(c string) bool {
	return len(c) == 40 && strings.Trim(c, "0") != ""
}

// checkCommits refuses versions whose commit differs from the one the lock pinned.
func checkCommits(pl plan.Plan, lk *lock.File) error {
	for _, p := range pl.Packages {
		lp, ok := lk.Get(p.Name)
		if !ok || lp.Commit == "" || lp.Version != p.Version.String() {
			continue
		}
		if lp.Commit != p.Commit {
			return &CommitChangedError{Package: p.Name, Version: lp.Version, Locked: lp.Commit, Now: p.Commit}
		}
	}
	return nil
}

// obtain makes sure the binary for p is in the cache: reuse it, take a
// verified prebuilt one, or build it from source, as the policy allows.
func (e Env) obtain(ctx context.Context, pl plan.Plan, p plan.Package, st settings.Settings, smap map[string]any) (origin, warning string, err error) {
	if entry, err := e.Cache.Entry(key(p)); err == nil {
		if entry.Origin == "source-build" {
			return "cached (built from source)", "", nil
		}
		return "cached", "", nil
	} else if e.Cache.Has(key(p)) {
		return "", "", &VerifyError{p.Name, "the cache entry is damaged (" + err.Error() + "); remove " + e.Cache.Dir(key(p))}
	}

	tried := []string{"cache"}
	if e.Local != nil && !e.Policy.SourceOnly {
		tried = append(tried, "local repository "+e.Local.Root)
		asset, file, err := e.Local.Find(p.Name, p.Version.String(), p.ID)
		switch {
		case err == nil:
			warning, err := verifyAsset(p, asset, file, true)
			if err != nil {
				return "", "", err
			}
			if err := e.Cache.Put(key(p), file, cache.Entry{Origin: "binary", Asset: asset}); err != nil {
				return "", "", &VerifyError{p.Name, err.Error()}
			}
			return "installed from the local repository", warning, nil
		case errors.Is(err, localrepo.ErrNoAssets), errors.Is(err, localrepo.ErrNoAsset):
			// no prebuilt binary: fall through to a source build
		default:
			return "", "", err
		}
	}

	if e.Policy.BinaryOnly || e.Fetch == nil {
		return "", "", &NoBinaryError{Package: p, Tried: tried, SourceDisabled: true}
	}
	tried = append(tried, "source build")
	e.progress("  %-24s %-10s no binary: building from source (this runs the package's build scripts)", p.Name, p.Version)
	if err := e.sourceBuild(ctx, pl, p, st, smap); err != nil {
		return "", "", &SourceBuildError{Package: p, Err: err}
	}
	return "built from source", "", nil
}

// sourceBuild fetches, builds and packs p, and puts the result in the cache.
func (e Env) sourceBuild(ctx context.Context, pl plan.Plan, p plan.Package, st settings.Settings, smap map[string]any) error {
	if err := e.checkToolchain(st); err != nil {
		return err
	}
	repo, err := source.Repository(p.Manifest)
	if err != nil {
		return err
	}
	cached, err := e.ensureSource(ctx, p, true)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(e.Cache.Root, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(e.Cache.Root, ".build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	// Builds run in a copy: some write into their source directory, and the
	// cached checkout (and the vendor copies made from it) must stay as fetched.
	srcDir := filepath.Join(work, "src")
	if err := source.Copy(e.Cache.SourceDir(p.Name, p.Version.String()), srcDir); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(srcDir, cache.SourceFile))

	var deps []string
	for _, dep := range closure(pl, p) {
		abs, err := filepath.Abs(e.Cache.Dir(key(dep)))
		if err != nil {
			return err
		}
		deps = append(deps, abs)
	}

	prefix := filepath.Join(work, "prefix")
	logPath := filepath.Join(e.Cache.Root, "logs", fmt.Sprintf("%s-%s-%s.log", strings.ReplaceAll(p.Name, "/", "_"), p.Version, p.ID[:12]))
	if err := build.Run(ctx, build.Params{
		Package: p.Name, Build: derefBuild(p.Manifest.Build), Settings: smap, Options: p.Options,
		SourceDir: srcDir, Prefix: prefix, WorkDir: filepath.Join(work, "build"),
		DepPrefixes: deps, Toolchain: e.Toolchain, Jobs: e.Jobs, LogPath: logPath, Echo: e.BuildOutput,
	}); err != nil {
		return err
	}

	// package the result exactly as `kiln package` would
	info, err := pack.Describe(p.Manifest, p.Version.String(), prefix, smap, p.Options, p.Requires)
	if err != nil {
		return fmt.Errorf("the build did not produce what the manifest promises: %w", err)
	}
	infoJSON, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	archivePath := filepath.Join(work, "package.tar.gz")
	if err := archive.Create(archivePath, prefix, map[string][]byte{manifest.PackageInfoFile: append(infoJSON, '\n')}); err != nil {
		return err
	}
	sum, size, err := archive.SHA256(archivePath)
	if err != nil {
		return err
	}
	asset := manifest.Asset{
		ID: p.ID, Settings: smap, Options: p.Options,
		File:        manifest.File{URL: "source-build", SHA256: sum, Size: size},
		Attestation: manifest.Attestation{Type: "unsigned"},
	}
	if len(p.Requires) > 0 {
		asset.Dependencies = map[string]manifest.AssetDep{}
		for _, r := range p.Requires {
			dp, _ := pl.Get(r)
			asset.Dependencies[r] = manifest.AssetDep{Version: dp.Version.String(), ID: dp.ID}
		}
	}
	return e.Cache.Put(key(p), archivePath, cache.Entry{
		Origin: "source-build",
		Source: &cache.SourceRef{Repository: repo, Commit: cached.Commit},
		Asset:  asset,
	})
}

func derefBuild(b *manifest.Build) manifest.Build {
	if b == nil {
		return manifest.Build{}
	}
	return *b
}

// packagesToBuild returns the packages that will need a source build: not
// cached, and without a usable prebuilt binary, if source builds are allowed.
func (e Env) packagesToBuild(pl plan.Plan) ([]plan.Package, error) {
	if e.Policy.BinaryOnly || e.Fetch == nil {
		return nil, nil
	}
	var out []plan.Package
	for _, p := range pl.Packages {
		if e.Cache.Has(key(p)) {
			continue
		}
		if e.Local != nil && !e.Policy.SourceOnly {
			_, _, err := e.Local.Find(p.Name, p.Version.String(), p.ID)
			if err == nil {
				continue // a prebuilt binary will be used
			}
			if !errors.Is(err, localrepo.ErrNoAssets) && !errors.Is(err, localrepo.ErrNoAsset) {
				return nil, err
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// checkRequirements checks what the machine needs: the requirements of using every
// package in pkgs (system-requires, also for cached or prebuilt ones) and, for the
// packages in toBuild, those of building it, implicit ones included. It returns every
// failure, split into blocking and optional.
func (e Env) checkRequirements(pkgs, toBuild []plan.Package, osName string, smap map[string]any) (required, optional []sysreq.Failure) {
	sys := sysreq.SystemEnv(osName)
	if e.SysReq != nil {
		sys = *e.SysReq
	}
	building := map[string]bool{}
	for _, p := range toBuild {
		building[p.Name] = true
	}
	var all []sysreq.Failure
	for _, p := range pkgs {
		ctx := when.Context(smap, p.Options)
		if building[p.Name] {
			reqs := sysreq.Implicit(p.Manifest)
			if p.Manifest.Build != nil {
				reqs = append(reqs, p.Manifest.Build.Requires...)
			}
			for _, r := range sysreq.Check(reqs, ctx, sys) {
				if !r.OK {
					all = append(all, sysreq.Failure{Package: p.Name, Version: p.Version.String(), Result: r})
				}
			}
		}
		for _, r := range sysreq.Check(p.Manifest.SystemRequires, ctx, sys) {
			if !r.OK {
				all = append(all, sysreq.Failure{Package: p.Name, Version: p.Version.String(), Use: true, Result: r})
			}
		}
	}
	return sysreq.Split(all)
}

// closure returns every package p depends on, directly or not, dependencies first.
func closure(pl plan.Plan, p plan.Package) []plan.Package {
	seen := map[string]bool{}
	var out []plan.Package
	var visit func(q plan.Package)
	visit = func(q plan.Package) {
		for _, r := range q.Requires {
			if seen[r] {
				continue
			}
			seen[r] = true
			if dp, ok := pl.Get(r); ok {
				visit(dp)
				out = append(out, dp)
			}
		}
	}
	visit(p)
	return out
}

// checkToolchain refuses a source build whose requested settings describe a
// different toolchain than the one that would run: the binary would then not
// be what its id says.
func (e Env) checkToolchain(want settings.Settings) error {
	if e.Detected == nil {
		return nil
	}
	d := e.Detected
	var diffs []string
	add := func(name, got, wanted string) {
		if got != wanted {
			diffs = append(diffs, fmt.Sprintf("%s: this machine has %q, the settings say %q", name, got, wanted))
		}
	}
	add("os", d.OS, want.OS)
	add("arch", d.Arch, want.Arch)
	add("compiler", d.Compiler.Name+" "+d.Compiler.Version, want.Compiler.Name+" "+want.Compiler.Version)
	add("stdlib", d.Stdlib, want.Stdlib)
	add("libc", d.Libc, want.Libc)
	if (d.CXX11ABI == nil) != (want.CXX11ABI == nil) || (d.CXX11ABI != nil && *d.CXX11ABI != *want.CXX11ABI) {
		diffs = append(diffs, "cxx11_abi differs from this machine's toolchain")
	}
	if len(diffs) > 0 {
		return fmt.Errorf("cannot build here for those settings (the result would not be the binary the id describes):\n    %s", strings.Join(diffs, "\n    "))
	}
	return nil
}

func (e Env) load(pl plan.Plan) ([]integration.Installed, error) {
	var out []integration.Installed
	for _, p := range pl.Packages {
		root, err := filepath.Abs(e.Cache.Dir(key(p)))
		if err != nil {
			return nil, err
		}
		if err := e.redescribe(p, root); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(filepath.Join(root, manifest.PackageInfoFile))
		if err != nil {
			return nil, &VerifyError{p.Name, "the package has no " + manifest.PackageInfoFile}
		}
		var info manifest.PackageInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			return nil, &VerifyError{p.Name, manifest.PackageInfoFile + ": " + err.Error()}
		}
		if info.Name != p.Name || info.Version != p.Version.String() {
			return nil, &VerifyError{p.Name, fmt.Sprintf("%s describes %s %s", manifest.PackageInfoFile, info.Name, info.Version)}
		}
		out = append(out, integration.Installed{Info: info, Root: root})
	}
	return out, nil
}

// redescribe rewrites the target description of a package kiln built itself
// from the current manifest, so that fixing which libraries, include
// directories or defines a target has does not need a rebuild: the files are
// already there, only their description changes. Downloaded packages keep the
// description they were published with.
func (e Env) redescribe(p plan.Package, root string) error {
	entry, err := e.Cache.Entry(key(p))
	if err != nil || entry.Origin != "source-build" {
		return nil
	}
	info, err := pack.Describe(p.Manifest, p.Version.String(), root, entry.Asset.Settings, entry.Asset.Options, p.Requires)
	if err != nil {
		return &VerifyError{p.Name, "the cached build does not match the manifest's targets (" + err.Error() + "); remove " + e.Cache.Dir(key(p)) + " to build again"}
	}
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, manifest.PackageInfoFile), append(raw, '\n'), 0o644)
}

func key(p plan.Package) cache.Key {
	return cache.Key{Name: p.Name, Version: p.Version.String(), ID: p.ID}
}

// ensureSource makes sure the source of p is in the cache and returns its
// record. A cached checkout is reused unless the registry pins another commit
// for this version. note says whether to mention an unpinned commit.
func (e Env) ensureSource(ctx context.Context, p plan.Package, note bool) (cache.SourceInfo, error) {
	version := p.Version.String()
	if si, err := e.Cache.Source(p.Name, version); err == nil && (!pinned(p.Commit) || si.Commit == p.Commit) {
		return si, nil
	}
	if e.Fetch == nil {
		return cache.SourceInfo{}, fmt.Errorf("source of %s %s is not cached and fetching is disabled", p.Name, version)
	}
	repo, err := source.Repository(p.Manifest)
	if err != nil {
		return cache.SourceInfo{}, err
	}
	tmp, err := e.Cache.NewSourceTemp()
	if err != nil {
		return cache.SourceInfo{}, err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "src")
	fetched, err := e.Fetch.Fetch(ctx, source.Request{
		Package: p.Name, Version: version, Repository: repo,
		Tags: source.Tags(source.TagTemplate(p.Manifest), version), Commit: p.Commit,
		Submodules: p.Manifest.Repository != nil && p.Manifest.Repository.Submodules,
	}, dir)
	if err != nil {
		return cache.SourceInfo{}, err
	}
	if note && !fetched.Pinned {
		e.progress("  %-24s %-10s note: the registry did not pin a commit, using %s from tag %s", p.Name, p.Version, fetched.Commit[:12], fetched.Tag)
	}
	si := cache.SourceInfo{Name: p.Name, Version: version, Repository: repo, Tag: fetched.Tag, Commit: fetched.Commit}
	if err := e.Cache.PutSource(dir, si); err != nil {
		return cache.SourceInfo{}, err
	}
	return si, nil
}

func pinned(commit string) bool { return realCommit(commit) }

// vendorSources makes vendor/<vendor>/<name> hold a plain copy of the
// source of every installed package, for reading and debugging. A directory
// that already holds the right version is left alone (so it keeps any edits);
// directories of packages that are no longer installed are removed. Failures
// are reported as warnings: the libraries work without the sources.
func (e Env) vendorSources(ctx context.Context, outDir string, pl plan.Plan) (warnings []string) {
	if e.Fetch == nil {
		return nil
	}
	want := map[string]bool{}
	for _, p := range pl.Packages {
		want[p.Name] = true
		dst := filepath.Join(outDir, filepath.FromSlash(p.Name))
		version := p.Version.String()
		if have, err := cache.ReadSourceInfo(dst); err == nil && have.Version == version && (!pinned(p.Commit) || have.Commit == p.Commit) {
			continue
		}
		_, err := e.ensureSource(ctx, p, false)
		if err == nil {
			err = replaceDir(dst, e.Cache.SourceDir(p.Name, version))
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s %s: the source could not be put in %s: %v", p.Name, version, outDir, err))
			continue
		}
	}
	// remove what is no longer needed, but only directories kiln made
	vendors, _ := os.ReadDir(outDir)
	for _, v := range vendors {
		if !v.IsDir() {
			continue
		}
		pkgs, _ := os.ReadDir(filepath.Join(outDir, v.Name()))
		for _, d := range pkgs {
			dir := filepath.Join(outDir, v.Name(), d.Name())
			if _, err := cache.ReadSourceInfo(dir); err == nil && !want[v.Name()+"/"+d.Name()] {
				_ = os.RemoveAll(dir)
			}
		}
		_ = os.Remove(filepath.Join(outDir, v.Name())) // only succeeds when empty
	}
	return warnings
}

// replaceDir makes dst a copy of src, replacing what was there, but only if
// what was there is a copy kiln made: vendor/ may hold other people's code.
func replaceDir(dst, src string) error {
	if _, err := os.Lstat(dst); err == nil {
		if _, err := cache.ReadSourceInfo(dst); err != nil {
			return fmt.Errorf("%s exists and was not created by kiln; not touching it", dst)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	next := filepath.Join(tmp, "src")
	if err := source.Copy(src, next); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(next, dst)
}

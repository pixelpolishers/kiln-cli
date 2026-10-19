package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/localrepo"
	"github.com/pixelpolishers/kiln-cli/internal/pack"
	"github.com/pixelpolishers/kiln-cli/internal/project"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
)

// errNoName is reported where a package name is genuinely needed.
var errNoName = fmt.Errorf("%s has no \"name\": a project that only uses libraries does not need one, but a package must be named (vendor/name) to be packaged or published", project.ManifestFile)

func cmdPackage(args []string, io IO) int {
	fs := newFlagSet("package", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln package --prefix DIR --version VERSION [--out dist] [-d DIR] [-s key=value]... [-o vendor/pkg:option=value]...")
		fmt.Fprintln(io.Err, "\nPacks an already built and installed tree (the prefix) as one binary configuration of the package")
		fmt.Fprintln(io.Err, "in DIR/kiln.json. Writes <out>/<name>-<version>-<id>.tar.gz and <out>/<id>.asset.json.")
		fmt.Fprintln(io.Err, "Building from source is not implemented yet, so the prefix must exist already.")
		printFlags(fs, io.Err)
	}
	common := addCommon(fs)
	dir := fs.String("dir", ".", "package directory `DIR` (contains kiln.json)")
	alias(fs, "d", "dir")
	prefix := fs.String("prefix", "", "the installed build tree to package `DIR`")
	version := fs.String("version", "", "`VERSION` to package (a git tag is not read yet)")
	out := fs.String("out", "dist", "output directory")
	var sflags, oflags stringList
	fs.Var(&sflags, "setting", "override a build setting `key=value` (repeatable)")
	alias(fs, "s", "setting")
	fs.Var(&oflags, "option", "set an option of this package or a dependency `vendor/pkg:option=value` (repeatable)")
	alias(fs, "o", "option")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 || *prefix == "" || *version == "" {
		return usageErr(io, fs, "package needs --prefix and --version")
	}

	st, _, code := loadSettings(io, sflags)
	if code >= 0 {
		return code
	}
	reqs, err := parseOptionFlags(oflags)
	if err != nil {
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitUsage
	}
	env, v, err := installEnv(common.registry, "", st.OS, io)
	if err != nil {
		return fail(io, err)
	}
	proj, err := project.Load(*dir, v)
	if err != nil {
		return fail(io, err)
	}
	if proj.Manifest.Name == "" {
		return fail(io, errNoName)
	}
	res, err := pack.Package(context.Background(), pack.Env{Source: env.Source, Validator: v}, pack.Params{
		Project: proj, Version: *version, Prefix: *prefix, OutDir: *out, Settings: st, Requests: reqs,
	})
	if err != nil {
		return failInstall(io, err)
	}
	fmt.Fprintf(io.Out, "packaged %s %s\n  id:      %s\n  archive: %s\n  sidecar: %s\n", proj.Manifest.Name, res.Version, res.ID, res.ArchivePath, res.SidecarPath)
	return exitOK
}

// publishTarget returns the package name and normalized version a publish or
// finalize command acts on.
func publishTarget(io IO, dir, version string) (manifest.Package, string, *manifest.Validator, int) {
	v, err := manifest.NewValidator()
	if err != nil {
		return manifest.Package{}, "", nil, fail(io, err)
	}
	proj, err := project.Load(dir, v)
	if err != nil {
		return manifest.Package{}, "", nil, fail(io, err)
	}
	if proj.Manifest.Name == "" {
		return manifest.Package{}, "", nil, fail(io, errNoName)
	}
	nv, err := semver.Normalize(version)
	if err != nil {
		fmt.Fprintf(io.Err, "kiln: --version: %v\n", err)
		return manifest.Package{}, "", nil, exitUsage
	}
	return proj.Manifest, nv, v, -1
}

func cmdPublish(args []string, io IO) int {
	fs := newFlagSet("publish", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln publish --local REPO --version VERSION [--dist dist] [-d DIR]")
		fmt.Fprintln(io.Err, "\nCopies the archives and sidecars of the given version from the dist directory into a local")
		fmt.Fprintln(io.Err, "repository. Run 'kiln finalize' afterwards to make them installable.")
		printFlags(fs, io.Err)
	}
	dir := fs.String("dir", ".", "package directory `DIR` (contains kiln.json)")
	alias(fs, "d", "dir")
	dist := fs.String("dist", "dist", "directory written by 'kiln package'")
	version := fs.String("version", "", "`VERSION` to publish")
	var local string
	localFlag(fs, &local)
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 || local == "" || *version == "" {
		return usageErr(io, fs, "publish needs --local (or $KILN_LOCAL_REPO) and --version")
	}
	m, nv, v, code := publishTarget(io, *dir, *version)
	if code >= 0 {
		return code
	}

	sidecars, err := filepath.Glob(filepath.Join(*dist, "*"+localrepo.SidecarSuffix))
	if err != nil {
		return fail(io, err)
	}
	prefix := strings.ReplaceAll(m.Name, "/", "_") + "-" + nv + "-"
	repo := localrepo.Repo{Root: local, Validator: v}
	added, skipped := 0, 0
	for _, sc := range sidecars {
		a, err := readSidecar(sc)
		if err != nil {
			return fail(io, err)
		}
		if !strings.HasPrefix(a.File.URL, prefix) {
			continue // another package or version in the same dist directory
		}
		ok, err := repo.Add(m.Name, nv, filepath.Join(*dist, a.File.URL), sc)
		if err != nil {
			return fail(io, err)
		}
		if ok {
			added++
		} else {
			skipped++
		}
	}
	if added+skipped == 0 {
		return fail(io, fmt.Errorf("no packaged files for %s %s in %s (run kiln package first)", m.Name, nv, *dist))
	}
	fmt.Fprintf(io.Out, "published %d file(s) of %s %s to %s (%d already present)\n", added, m.Name, nv, local, skipped)
	return exitOK
}

func cmdFinalize(args []string, io IO) int {
	fs := newFlagSet("finalize", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln finalize --local REPO --version VERSION [--commit SHA] [-d DIR]")
		fmt.Fprintln(io.Err, "\nAssembles <repo>/<name>/<version>/kiln-assets.json from the published sidecars. Run it once after")
		fmt.Fprintln(io.Err, "all configurations are published; run it again after publishing more.")
		printFlags(fs, io.Err)
	}
	dir := fs.String("dir", ".", "package directory `DIR` (contains kiln.json)")
	alias(fs, "d", "dir")
	version := fs.String("version", "", "`VERSION` to finalize")
	commit := fs.String("commit", "", "git commit `SHA` the binaries were built from (default: git rev-parse HEAD in DIR)")
	var local string
	localFlag(fs, &local)
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 || local == "" || *version == "" {
		return usageErr(io, fs, "finalize needs --local (or $KILN_LOCAL_REPO) and --version")
	}
	m, nv, v, code := publishTarget(io, *dir, *version)
	if code >= 0 {
		return code
	}
	if m.Repository == nil || m.Repository.URL == "" {
		return fail(io, fmt.Errorf("%s needs a repository.url: the asset manifest records where the source came from", project.ManifestFile))
	}
	sha := *commit
	if sha == "" {
		out, err := exec.Command("git", "-C", *dir, "rev-parse", "HEAD").Output()
		if err != nil {
			return fail(io, fmt.Errorf("cannot read the git commit (pass --commit): %w", err))
		}
		sha = strings.TrimSpace(string(out))
	}
	repo := localrepo.Repo{Root: local, Validator: v}
	n, err := repo.Finalize(m.Name, nv, manifest.AssetSource{Repository: m.Repository.URL, Commit: sha})
	if err != nil {
		return fail(io, err)
	}
	fmt.Fprintf(io.Out, "wrote %s with %d asset(s) for %s %s\n", localrepo.AssetManifestFile, n, m.Name, nv)
	return exitOK
}

func readSidecar(path string) (manifest.Asset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest.Asset{}, err
	}
	var a manifest.Asset
	if err := jsonUnmarshal(raw, &a); err != nil {
		return manifest.Asset{}, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

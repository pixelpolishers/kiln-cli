// Package pack implements the publisher side: turning an installed build tree
// into a package archive with its asset metadata.
//
// Building from source is not implemented yet; the tree to package (the
// "prefix") is built by hand or by the project's own build and passed in.
package pack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/archive"
	"github.com/pixelpolishers/kiln-cli/internal/plan"
	"github.com/pixelpolishers/kiln-cli/internal/project"
	"github.com/pixelpolishers/kiln-cli/internal/registrysrc"
	"github.com/pixelpolishers/kiln-cli/internal/settings"
	"github.com/pixelpolishers/kiln-core/assetid"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/options"
	"github.com/pixelpolishers/kiln-core/semver"
	"github.com/pixelpolishers/kiln-core/solver"
	"github.com/pixelpolishers/kiln-core/when"
)

type Env struct {
	Source    *registrysrc.Source
	Validator *manifest.Validator
}

type Params struct {
	Project  project.Project
	Version  string // any form semver.Normalize accepts
	Prefix   string // the installed build tree to package
	OutDir   string
	Settings settings.Settings
	Requests []options.Request // -o overrides
}

type Result struct {
	ID          string
	Version     string
	ArchivePath string
	SidecarPath string
	Asset       manifest.Asset
}

// Package packs p.Prefix. Dependencies are resolved (but not fetched) so their
// ids can be part of this package's id.
func Package(ctx context.Context, env Env, p Params) (Result, error) {
	m := p.Project.Manifest
	if m.Name == "" {
		return Result{}, fmt.Errorf("the manifest has no \"name\" (vendor/name), which a package needs")
	}
	version, err := semver.Normalize(p.Version)
	if err != nil {
		return Result{}, fmt.Errorf("version: %w", err)
	}
	if st, err := os.Stat(p.Prefix); err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("prefix %s is not a directory", p.Prefix)
	}
	smap, err := p.Settings.Map()
	if err != nil {
		return Result{}, err
	}

	// dependencies: resolve and plan, to learn the exact builds we link against
	roots, err := project.Roots(m.Require, p.Settings.OS)
	if err != nil {
		return Result{}, err
	}
	sol, err := solver.Solve(ctx, env.Source, roots, solver.Options{})
	if err != nil {
		return Result{}, err
	}
	pl, err := plan.Build(ctx, env.Source, plan.Input{Solution: sol, Settings: smap, Requests: project.OptionRequests(m.Require)})
	if err != nil {
		return Result{}, err
	}

	// this package's own options: defaults plus -o overrides aimed at it
	var own []options.Request
	for _, r := range p.Requests {
		if r.Package == m.Name {
			own = append(own, r)
		}
	}
	resolved, err := options.Resolve(map[string]map[string]manifest.Option{m.Name: m.Options}, own)
	if err != nil {
		return Result{}, &plan.OptionsError{Err: err}
	}
	opts := resolved[m.Name]

	deps := map[string]assetid.Dep{}
	direct := []string{}
	for _, r := range roots {
		dp, ok := pl.Get(r.Name)
		if !ok {
			continue
		}
		deps[r.Name] = assetid.Dep{Version: dp.Version.String(), ID: dp.ID}
		direct = append(direct, r.Name)
	}
	kind := plan.KindOf(m)
	id, err := assetid.Compute(assetid.Input{
		Package: m.Name, Version: version,
		Settings: assetid.Relevant(smap, kind), Options: opts, Deps: deps,
	})
	if err != nil {
		return Result{}, err
	}

	info, err := Describe(m, version, p.Prefix, smap, opts, direct)
	if err != nil {
		return Result{}, err
	}
	infoJSON, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return Result{}, err
	}

	if err := os.MkdirAll(p.OutDir, 0o755); err != nil {
		return Result{}, err
	}
	fileName := fmt.Sprintf("%s-%s-%s.tar.gz", strings.ReplaceAll(m.Name, "/", "_"), version, id[:12])
	archivePath := filepath.Join(p.OutDir, fileName)
	if err := archive.Create(archivePath, p.Prefix, map[string][]byte{manifest.PackageInfoFile: append(infoJSON, '\n')}); err != nil {
		return Result{}, err
	}
	sum, size, err := archive.SHA256(archivePath)
	if err != nil {
		return Result{}, err
	}

	asset := manifest.Asset{
		ID: id, Settings: smap, Options: opts,
		File:        manifest.File{URL: fileName, SHA256: sum, Size: size},
		Attestation: manifest.Attestation{Type: "unsigned"},
	}
	if len(deps) > 0 {
		asset.Dependencies = map[string]manifest.AssetDep{}
		for n, d := range deps {
			asset.Dependencies[n] = manifest.AssetDep{Version: d.Version, ID: d.ID}
		}
	}
	sidecar, err := json.MarshalIndent(asset, "", "  ")
	if err != nil {
		return Result{}, err
	}
	sidecarPath := filepath.Join(p.OutDir, id+".asset.json")
	if err := os.WriteFile(sidecarPath, append(sidecar, '\n'), 0o644); err != nil {
		return Result{}, err
	}
	return Result{ID: id, Version: version, ArchivePath: archivePath, SidecarPath: sidecarPath, Asset: asset}, nil
}

// Describe resolves the manifest's targets for one configuration and checks
// that the prefix really contains what they promise. direct are the packages
// this package depends on (directly) in this configuration.
func Describe(m manifest.Package, version, prefix string, smap, opts map[string]any, direct []string) (manifest.PackageInfo, error) {
	ctx := when.Context(smap, opts)
	linkage, _ := smap["linkage"].(string)
	info := manifest.PackageInfo{Name: m.Name, Version: version, Targets: map[string]manifest.TargetInfo{}}

	targetNames := make([]string, 0, len(m.Targets))
	for n := range m.Targets {
		targetNames = append(targetNames, n)
	}
	sort.Strings(targetNames)

	isDirect := map[string]bool{}
	for _, d := range direct {
		isDirect[d] = true
	}

	for _, name := range targetNames {
		t := m.Targets[name]
		if !when.Match(t.When, ctx) {
			continue // not part of this configuration (e.g. GUI libraries when options.gui is off)
		}
		ti := manifest.TargetInfo{Type: t.Type, Defines: t.Defines}

		if t.Type != "executable" && t.Type != "module" {
			for _, inc := range t.Include {
				if when.Match(inc.When, ctx) {
					ti.Include = append(ti.Include, inc.Value)
				}
			}
			if len(ti.Include) == 0 {
				ti.Include = []string{"include"}
			}
			for _, inc := range ti.Include {
				if st, err := os.Stat(filepath.Join(prefix, filepath.FromSlash(inc))); err != nil || !st.IsDir() {
					return info, fmt.Errorf("target %s: include directory %q not found in %s", name, inc, prefix)
				}
			}
		}

		switch t.Type {
		case "library", "static-library", "dynamic-library":
			want := linkage
			if t.Type == "static-library" {
				want = "static"
			} else if t.Type == "dynamic-library" {
				want = "shared"
			}
			base := t.OutputName
			if base == "" {
				base = name
			}
			lib, err := findLibrary(prefix, base, want, smap["build_type"] == "debug")
			if err != nil {
				return info, fmt.Errorf("target %s: %w", name, err)
			}
			ti.Libs = []string{lib}
		}

		if t.Type == "executable" {
			base := t.OutputName
			if base == "" {
				base = name
			}
			exe, err := findExecutable(prefix, base)
			if err != nil {
				return info, fmt.Errorf("target %s: %w", name, err)
			}
			ti.Exe = exe
		}

		for _, l := range t.LinkLibraries {
			if !when.Match(l.When, ctx) {
				continue
			}
			if _, sibling := m.Targets[l.Value]; sibling {
				ti.Uses = append(ti.Uses, l.Value)
			} else {
				ti.Links = append(ti.Links, l.Value)
			}
		}

		reqNames := t.Requires
		if len(reqNames) == 0 {
			reqNames = direct
		}
		for _, r := range reqNames {
			if _, declared := m.Require[r]; !declared {
				return info, fmt.Errorf("target %s requires %s, which is not in the package's require section", name, r)
			}
			if !isDirect[r] {
				continue // optional or for another platform: not part of this configuration
			}
			ti.Requires = append(ti.Requires, manifest.TargetRequire{Package: r, Targets: m.Require[r].Targets})
		}
		info.Targets[name] = ti
	}
	return info, nil
}

// findExecutable looks for the program of an executable target under bin/ (Qt keeps its code generators in libexec/).
func findExecutable(prefix, base string) (string, error) {
	for _, dir := range []string{"bin", "libexec"} {
		for _, n := range []string{base, base + ".exe"} {
			if st, err := os.Stat(filepath.Join(prefix, dir, n)); err == nil && st.Mode().IsRegular() {
				return dir + "/" + n, nil
			}
		}
	}
	return "", fmt.Errorf("no program %q found under bin/ or libexec/", base)
}

// findLibrary looks for the library file of a target under lib/ and lib64/.
// Debug builds of many libraries add a "d" to the name (fmt: libfmtd.a).
// An output name may carry a subdirectory of lib/ ("opencv4/3rdparty/zlib").
func findLibrary(prefix, base, linkage string, debug bool) (string, error) {
	sub := ""
	if i := strings.LastIndex(base, "/"); i >= 0 {
		sub, base = base[:i+1], base[i+1:]
	}
	bases := []string{base}
	if debug {
		bases = []string{base + "d", base}
	}
	var names []string
	for _, b := range bases {
		switch linkage {
		case "static":
			names = append(names, "lib"+b+".a", b+".lib")
		case "shared":
			names = append(names, "lib"+b+".so", "lib"+b+".dylib")
		default:
			return "", fmt.Errorf("unknown linkage %q", linkage)
		}
	}
	for _, dir := range []string{"lib", "lib64"} {
		for _, n := range names {
			if st, err := os.Stat(filepath.Join(prefix, dir, filepath.FromSlash(sub), n)); err == nil && st.Mode().IsRegular() {
				return dir + "/" + sub + n, nil
			}
		}
	}
	return "", fmt.Errorf("no %s library %q found under lib/ or lib64/ (looked for %s)", linkage, base, strings.Join(names, ", "))
}

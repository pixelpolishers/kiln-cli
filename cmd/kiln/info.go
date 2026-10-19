package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/pixelpolishers/kiln-cli/internal/client"
	"github.com/pixelpolishers/kiln-core/api"
	"github.com/pixelpolishers/kiln-core/constraint"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
)

func cmdInfo(args []string, io IO) int {
	fs := newFlagSet("info", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln info <vendor/pkg>[:<version>] [--registry URL] [--json]")
	}
	common := addCommon(fs)
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return usageErr(io, fs, "info takes exactly one package")
	}
	name, spec := splitNameVersion(pos[0])
	if spec != "" {
		if _, err := constraint.Parse(spec); err != nil {
			return usageErr(io, fs, err.Error())
		}
	}

	ctx := context.Background()
	c := client.New(common.registry)
	pkg, err := c.Package(ctx, name)
	if err != nil {
		if client.IsNotFound(err) {
			return fail(io, fmt.Errorf("package %s not found", name))
		}
		return fail(io, err)
	}
	want, err := pickVersion(pkg, spec)
	if err != nil {
		return fail(io, err)
	}
	ver, err := c.Version(ctx, name, want)
	if err != nil {
		if client.IsNotFound(err) {
			return fail(io, fmt.Errorf("%s has no version %s (available: %s)", name, want, versionList(pkg)))
		}
		return fail(io, err)
	}

	if common.json {
		enc := json.NewEncoder(io.Out)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"package": pkg, "version": ver})
		return exitOK
	}

	m, err := manifest.ParsePackage(ver.Manifest)
	if err != nil {
		return fail(io, fmt.Errorf("registry returned an unreadable manifest: %w", err))
	}
	printInfo(io, pkg, ver, m)
	return exitOK
}

// pickVersion chooses the version to show: the latest if spec is empty, an
// exact version as given (even if flagged, so it can be inspected), otherwise
// the newest unflagged version matching the constraint.
func pickVersion(pkg api.PackageDetail, spec string) (string, error) {
	if spec == "" {
		if pkg.Latest == "" {
			return "", fmt.Errorf("%s has no installable version", pkg.Name)
		}
		return pkg.Latest, nil
	}
	if exact, err := semver.Normalize(spec); err == nil {
		for _, v := range pkg.Versions {
			if v.Version == exact {
				return exact, nil
			}
		}
		return "", fmt.Errorf("%s has no version %s (available: %s)", pkg.Name, exact, versionList(pkg))
	}
	c, err := constraint.Parse(spec)
	if err != nil {
		return "", err
	}
	for _, v := range pkg.Versions { // newest first
		if sv, err := semver.Parse(v.Version); err == nil && !v.Flagged && c.Matches(sv) {
			return v.Version, nil
		}
	}
	return "", fmt.Errorf("no version of %s matches %s (available: %s)", pkg.Name, spec, versionList(pkg))
}

func versionList(p api.PackageDetail) string {
	vs := make([]string, 0, len(p.Versions))
	for _, v := range p.Versions {
		vs = append(vs, v.Version)
	}
	return strings.Join(vs, ", ")
}

func printInfo(io IO, pkg api.PackageDetail, ver api.VersionDetail, m manifest.Package) {
	w := io.Out
	fmt.Fprintf(w, "%s %s\n", pkg.Name, ver.Version)
	if pkg.Description != "" {
		fmt.Fprintln(w, pkg.Description)
	}
	fmt.Fprintln(w)
	if pkg.License != "" {
		fmt.Fprintf(w, "License:   %s\n", pkg.License)
	}
	if m.Language != "" {
		fmt.Fprintf(w, "Language:  %s\n", m.Language)
	}
	fmt.Fprintf(w, "Versions:  %s (latest: %s)\n", versionList(pkg), pkg.Latest)
	flag := ""
	if ver.Flagged {
		flag = "  FLAGGED: the tag moved after publishing; do not use"
	}
	fmt.Fprintf(w, "Commit:    %.12s%s\n", ver.Commit, flag)
	if m.Build != nil {
		fmt.Fprintf(w, "Build:     %s\n", m.Build.System)
	}
	for _, p := range m.Providers {
		fmt.Fprintf(w, "Provider:  %s\n", providerText(p))
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(m.Targets) > 0 {
		fmt.Fprintln(w, "\nTargets")
		for _, n := range sortedKeys(m.Targets) {
			t := m.Targets[n]
			extra := ""
			if len(t.Requires) > 0 {
				extra = "requires " + strings.Join(t.Requires, ", ")
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", n, t.Type, extra)
		}
		tw.Flush()
	}
	if len(m.Require) > 0 {
		fmt.Fprintln(w, "\nRequires")
		for _, n := range sortedKeys(m.Require) {
			d := m.Require[n]
			extra := ""
			if len(d.Targets) > 0 {
				extra = "targets: " + strings.Join(d.Targets, ", ")
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", n, d.Version, extra)
		}
		tw.Flush()
	}
	if len(m.Options) > 0 {
		fmt.Fprintln(w, "\nOptions")
		for _, n := range sortedKeys(m.Options) {
			o := m.Options[n]
			fmt.Fprintf(tw, "  %s\tdefault %v\t%s\n", n, o.Default, o.Description)
		}
		tw.Flush()
	}
	fmt.Fprintln(w, "\nBinary coverage: not available yet (the CLI does not query providers yet)")
}

func providerText(p manifest.Provider) string {
	switch p.Type {
	case "github":
		s := "github"
		if p.Repository != "" {
			s += " " + p.Repository
		}
		if p.Tag != "" {
			s += " (tag " + p.Tag + ")"
		}
		return s
	case "url":
		return "url " + p.URL
	case "local":
		return "local " + p.Path
	}
	return p.Type
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/pixelpolishers/kiln-cli/internal/client"
	"github.com/pixelpolishers/kiln-cli/internal/registrysrc"
	"github.com/pixelpolishers/kiln-cli/internal/settings"
	"github.com/pixelpolishers/kiln-core/constraint"
	"github.com/pixelpolishers/kiln-core/solver"
)

// cmdResolve runs the version-solving phase of install without installing
// anything: it shows which version of every package would be chosen.
func cmdResolve(args []string, io IO) int {
	fs := newFlagSet("resolve", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln resolve <vendor/pkg>[:<constraint>]... [-s os=...] [--registry URL] [--json]")
		fmt.Fprintln(io.Err, "\nShows the versions that would be installed, dependencies first. Without a constraint any stable version is accepted.")
		printFlags(fs, io.Err)
	}
	common := addCommon(fs)
	var overrides stringList
	fs.Var(&overrides, "setting", "override the target operating system `os=NAME`, e.g. -s os=windows (affects platform-specific dependencies)")
	alias(fs, "s", "setting")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) == 0 {
		return usageErr(io, fs, "resolve needs at least one package")
	}

	var roots []solver.Dep
	for _, arg := range pos {
		name, spec := splitNameVersion(arg)
		if spec == "" {
			spec = "*"
		}
		c, err := constraint.Parse(spec)
		if err != nil {
			return usageErr(io, fs, fmt.Sprintf("%s: %v", arg, err))
		}
		roots = append(roots, solver.Dep{Name: name, Constraint: c})
	}

	target := settings.Detect(settings.SystemEnv())
	if err := target.Settings.Apply(overrides, target.Sources); err != nil {
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitUsage
	}

	src := &registrysrc.Source{Client: client.New(common.registry), OS: target.Settings.OS}
	sol, err := solver.Solve(context.Background(), src, roots, solver.Options{})
	if err != nil {
		code := fail(io, err)
		if code == exitError {
			code = exitResolution
		}
		return code
	}

	if common.json {
		type pkg struct {
			Name     string   `json:"name"`
			Version  string   `json:"version"`
			Requires []string `json:"requires"`
		}
		out := make([]pkg, len(sol.Packages))
		for i, p := range sol.Packages {
			out[i] = pkg{p.Name, p.Version.String(), append([]string{}, p.Requires...)}
		}
		enc := json.NewEncoder(io.Out)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return exitOK
	}
	tw := tabwriter.NewWriter(io.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PACKAGE\tVERSION\tREQUIRES")
	for _, p := range sol.Packages {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, p.Version, strings.Join(p.Requires, ", "))
	}
	tw.Flush()
	return exitOK
}

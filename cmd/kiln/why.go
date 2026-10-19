package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/install"
)

// failLockRead reports the errors of the commands that only read kiln.lock.
func failLockRead(io IO, err error) int {
	var notInstalled *install.NotInstalledError
	if errors.As(err, &notInstalled) {
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitError
	}
	return failInstall(io, err)
}

func stepText(s install.Step) string {
	out := s.Name
	if s.Constraint != "" {
		out += " " + s.Constraint
	}
	if s.Version != "" {
		out += " (" + s.Version + ")"
	}
	return out
}

// cmdWhy explains why a package is installed.
func cmdWhy(args []string, io IO) int {
	fs := newFlagSet("why", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln why <vendor/pkg> [-d DIR] [--registry URL] [--json]")
		fmt.Fprintln(io.Err, "\nShows why a package is in kiln.lock: required by kiln.json itself, or through which other packages.")
		printFlags(fs, io.Err)
	}
	common := addCommon(fs)
	dir := fs.String("dir", ".", "project directory `DIR`")
	alias(fs, "d", "dir")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return usageErr(io, fs, "why needs exactly one package")
	}
	env, _, err := installEnv(common.registry, "", "", io)
	if err != nil {
		return fail(io, err)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return fail(io, err)
	}
	res, err := install.Why(context.Background(), env, abs, pos[0])
	if err != nil {
		return failLockRead(io, err)
	}
	if res.Stale {
		fmt.Fprintln(io.Err, "warning: kiln.json changed since kiln.lock was written; this explains the lock, run 'kiln update' to bring it up to date")
	}
	if common.json {
		type step struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			Constraint string `json:"constraint,omitempty"`
		}
		out := struct {
			Name    string   `json:"name"`
			Version string   `json:"version"`
			Direct  bool     `json:"direct"`
			Paths   [][]step `json:"paths"`
		}{res.Name, res.Version, res.Direct, [][]step{}}
		for _, p := range res.Paths {
			var steps []step
			for _, s := range p {
				steps = append(steps, step{s.Name, s.Version, s.Constraint})
			}
			out.Paths = append(out.Paths, steps)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(io.Out, string(b))
		return exitOK
	}
	fmt.Fprintf(io.Out, "%s %s is installed because:\n\n", res.Name, res.Version)
	for _, p := range res.Paths {
		parts := []string{"kiln.json"}
		for _, s := range p {
			parts = append(parts, stepText(s))
		}
		note := ""
		if len(p) == 1 {
			note = "   <- required directly"
		}
		fmt.Fprintf(io.Out, "  %s%s\n", strings.Join(parts, " -> "), note)
	}
	if len(res.Paths) == 0 {
		fmt.Fprintln(io.Out, "  (no path from kiln.json was found; the lock may be out of date, run 'kiln update')")
	}
	if len(res.Paths) >= 50 {
		fmt.Fprintln(io.Out, "\n  (only the first 50 paths are shown)")
	}
	return exitOK
}

// cmdWhyNot explains why a package (version) cannot be installed.
func cmdWhyNot(args []string, io IO) int {
	fs := newFlagSet("why-not", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln why-not <vendor/pkg>[:<constraint>] [-d DIR] [--registry URL] [--json]")
		fmt.Fprintln(io.Err, "\nExplains what keeps a package, or a version range of it, out of the project: the requirements in")
		fmt.Fprintln(io.Err, "kiln.json and of the locked packages. Changes nothing. Exit status 3 when it cannot be installed.")
		printFlags(fs, io.Err)
	}
	common := addCommon(fs)
	dir := fs.String("dir", ".", "project directory `DIR`")
	alias(fs, "d", "dir")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return usageErr(io, fs, "why-not needs exactly one package")
	}
	name, spec := splitNameVersion(pos[0])
	env, _, err := installEnv(common.registry, "", "", io)
	if err != nil {
		return fail(io, err)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return fail(io, err)
	}
	res, err := install.WhyNot(context.Background(), env, abs, name, spec)
	if err != nil {
		code := failLockRead(io, err)
		if code == exitError {
			code = exitUsage
		}
		return code
	}
	if res.Stale {
		fmt.Fprintln(io.Err, "warning: kiln.json changed since kiln.lock was written; run 'kiln update' to bring the lock up to date")
	}
	possible := len(res.Candidates) > 0 && len(res.Blockers) == 0 && res.SolveErr == nil
	if common.json {
		type blocker struct {
			By        string `json:"by"`
			ByVersion string `json:"by-version,omitempty"`
			Requires  string `json:"requires"`
			Allowing  string `json:"allowing,omitempty"`
		}
		out := struct {
			Package    string    `json:"package"`
			Constraint string    `json:"constraint"`
			Installed  string    `json:"installed,omitempty"`
			Possible   bool      `json:"possible"`
			Candidates []string  `json:"candidates"`
			Blockers   []blocker `json:"blockers"`
			Changes    []string  `json:"changes"`
			Error      string    `json:"error,omitempty"`
		}{res.Name, res.Spec, res.Installed, possible, append([]string{}, res.Candidates...), []blocker{}, append([]string{}, res.Changes...), ""}
		for _, b := range res.Blockers {
			by := b.By
			if by == "" {
				by = "kiln.json"
			}
			out.Blockers = append(out.Blockers, blocker{by, b.ByVersion, b.Requires, b.Allowing})
		}
		if res.SolveErr != nil {
			out.Error = res.SolveErr.Error()
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(io.Out, string(b))
	} else {
		printWhyNot(io, res, possible)
	}
	if possible {
		return exitOK
	}
	return exitResolution
}

func printWhyNot(io IO, res install.WhyNotResult, possible bool) {
	what := res.Name
	if res.Spec != "*" {
		what += " " + res.Spec
	}
	if len(res.Candidates) == 0 {
		fmt.Fprintf(io.Out, "No version of %s matches %s", res.Name, res.Spec)
		if len(res.Available) > 0 {
			fmt.Fprintf(io.Out, ": the registry has %s", strings.Join(res.Available, ", "))
		}
		fmt.Fprintln(io.Out, ".")
		return
	}
	installed := ""
	if res.Installed != "" {
		installed = fmt.Sprintf(" (locked: %s)", res.Installed)
	}
	if possible && res.Installed != "" && contains(res.Candidates, res.Installed) {
		fmt.Fprintf(io.Out, "%s is already satisfied: %s is locked.\n", what, res.Installed)
		return
	}
	if possible {
		fmt.Fprintf(io.Out, "Nothing prevents %s%s: %s is available.\n", what, installed, res.Candidates[0])
		printChanges(io, res)
		try := res.Name
		if res.Spec != "*" {
			try += ":" + res.Spec
		}
		fmt.Fprintf(io.Out, "\nTry: kiln require %s\n", try)
		return
	}
	fmt.Fprintf(io.Out, "%s cannot be installed with your current requirements%s.\n", what, installed)
	if len(res.Blockers) > 0 {
		fmt.Fprintln(io.Out, "\nBlocked by:")
		for _, b := range res.Blockers {
			if b.By == "" {
				fmt.Fprintf(io.Out, "  kiln.json requires %s %s\n", res.Name, b.Requires)
				continue
			}
			note := ""
			if b.Allowing != "" {
				note = fmt.Sprintf("   (%s %s would allow it)", b.By, b.Allowing)
			}
			fmt.Fprintf(io.Out, "  %s %s requires %s %s%s\n", b.By, b.ByVersion, res.Name, b.Requires, note)
		}
	}
	if res.SolveErr != nil {
		fmt.Fprintf(io.Out, "\nNo combination of versions works:\n%s\n", indent(res.SolveErr.Error(), "  "))
		return
	}
	// blockers exist, but other versions of them make it work
	fmt.Fprintln(io.Out, "\nA different set of versions would work; it needs these changes:")
	printChanges(io, res)
	fmt.Fprintln(io.Out, "\nChange the requirement in kiln.json if one is listed, then run 'kiln update'.")
}

func printChanges(io IO, res install.WhyNotResult) {
	if len(res.Changes) == 0 {
		return
	}
	fmt.Fprintln(io.Out, "\nInstalling it would change:")
	for _, c := range res.Changes {
		fmt.Fprintf(io.Out, "  %s\n", c)
	}
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

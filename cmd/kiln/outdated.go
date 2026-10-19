package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/pixelpolishers/kiln-cli/internal/install"
)

// cmdOutdated lists the locked packages for which the registry has a newer version.
func cmdOutdated(args []string, io IO) int {
	fs := newFlagSet("outdated", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln outdated [-d DIR] [--all] [--strict] [--registry URL] [--json]")
		fmt.Fprintln(io.Err, "\nCompares the versions in kiln.lock with the registry and changes nothing.")
		fmt.Fprintln(io.Err, "  WANTED  what 'kiln update' would install (the newest version your constraints allow)")
		fmt.Fprintln(io.Err, "  LATEST  the newest stable version in the registry, whatever the constraints say")
		printFlags(fs, io.Err)
	}
	common := addCommon(fs)
	dir := fs.String("dir", ".", "project directory `DIR`")
	alias(fs, "d", "dir")
	all := fs.Bool("all", false, "include packages that only other packages require")
	alias(fs, "a", "all")
	strict := fs.Bool("strict", false, fmt.Sprintf("exit with status %d when something is outdated (for CI)", exitOutdated))
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageErr(io, fs, "outdated takes no arguments")
	}

	env, _, err := installEnv(common.registry, "", "", io)
	if err != nil {
		return fail(io, err)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return fail(io, err)
	}
	res, err := install.Outdated(context.Background(), env, abs)
	if err != nil {
		return failInstall(io, err)
	}

	var shown []install.OutdatedPackage
	for _, p := range res.Packages {
		if p.Outdated() && (p.Direct || *all) {
			shown = append(shown, p)
		}
	}
	// packages that only another package requires are counted even when not listed
	hidden := 0
	for _, p := range res.Packages {
		if p.Outdated() && !p.Direct && !*all {
			hidden++
		}
	}

	if res.Stale {
		fmt.Fprintln(io.Err, "warning: kiln.json changed since kiln.lock was written; run 'kiln update' to bring the lock up to date")
	}
	if common.json {
		type row struct {
			Name     string `json:"name"`
			Direct   bool   `json:"direct"`
			Locked   string `json:"locked"`
			Wanted   string `json:"wanted"`
			Latest   string `json:"latest"`
			HeldBack bool   `json:"held-back"`
		}
		rows := []row{}
		for _, p := range shown {
			rows = append(rows, row{p.Name, p.Direct, p.Locked, p.Wanted, p.Latest, p.HeldBack()})
		}
		out, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(io.Out, string(out))
	} else {
		if len(shown) == 0 {
			fmt.Fprintln(io.Out, "Everything is up to date.")
		} else {
			tw := tabwriter.NewWriter(io.Out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PACKAGE\tLOCKED\tWANTED\tLATEST\t")
			for _, p := range shown {
				note := ""
				if p.HeldBack() {
					note = "newest version is blocked by version constraints"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Locked, dash(p.Wanted, p.Locked), dash(p.Latest, p.Locked), note)
			}
			tw.Flush()
			fmt.Fprintln(io.Out)
			fmt.Fprintf(io.Out, "%d package(s) can be updated with 'kiln update' (or 'kiln update vendor/pkg').\n", countWanted(shown))
		}
		if hidden > 0 {
			fmt.Fprintf(io.Out, "%d more package(s) required by other packages are outdated; --all shows them.\n", hidden)
		}
	}
	if *strict && len(shown) > 0 {
		return exitOutdated
	}
	return exitOK
}

// dash shows "-" where a column has nothing newer than the locked version.
func dash(v, locked string) string {
	if v == locked {
		return "-"
	}
	return v
}

func countWanted(ps []install.OutdatedPackage) int {
	n := 0
	for _, p := range ps {
		if p.Wanted != p.Locked {
			n++
		}
	}
	return n
}

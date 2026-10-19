package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pixelpolishers/kiln-cli/internal/audit"
	"github.com/pixelpolishers/kiln-cli/internal/osv"
)

// cmdAudit checks the locked packages against the OSV vulnerability database.
func cmdAudit(args []string, io IO) int {
	fs := newFlagSet("audit", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln audit [-d DIR] [--ignore ID]... [--registry URL] [--json]")
		fmt.Fprintln(io.Err, "\nChecks every package in kiln.lock against the OSV vulnerability database (osv.dev). The repository URL and")
		fmt.Fprintln(io.Err, "tag (or pinned commit) of each locked package are sent to the OSV service. Exit status 9 when advisories are found.")
		fmt.Fprintln(io.Err, "A match means the locked version is in the range the advisory lists, not that your use of the library is exploitable.")
		printFlags(fs, io.Err)
	}
	common := addCommon(fs)
	dir := fs.String("dir", ".", "project directory `DIR`")
	alias(fs, "d", "dir")
	var ignore stringList
	fs.Var(&ignore, "ignore", "accept the advisory `ID` (a CVE or OSV id), do not report it (repeatable)")
	osvURL := fs.String("osv-url", os.Getenv("KILN_OSV_URL"), "vulnerability database `URL` (default $KILN_OSV_URL or "+osv.DefaultURL+")")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageErr(io, fs, "audit takes no arguments")
	}

	env, v, err := installEnv(common.registry, "", "", io)
	if err != nil {
		return fail(io, err)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return fail(io, err)
	}
	res, err := audit.Run(context.Background(), env.Source, osv.New(*osvURL), v, audit.Options{Dir: abs, Ignore: ignore})
	if err != nil {
		var ne *osv.NetworkError
		switch {
		case errors.Is(err, audit.ErrNoLock):
			fmt.Fprintf(io.Err, "kiln: %v\n", err)
			return exitError
		case errors.As(err, &ne):
			fmt.Fprintf(io.Err, "kiln: %v\n", err)
			return exitNetwork
		}
		return failInstall(io, err)
	}

	if common.json {
		type vuln struct {
			ID       string   `json:"id"`
			Aliases  []string `json:"aliases,omitempty"`
			Severity string   `json:"severity,omitempty"`
			Summary  string   `json:"summary"`
			URL      string   `json:"url"`
		}
		type pkg struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			Query      string `json:"query,omitempty"`
			NotChecked string `json:"not-checked,omitempty"`
			Ignored    int    `json:"ignored,omitempty"`
			Vulns      []vuln `json:"vulnerabilities"`
		}
		out := struct {
			Advisories int   `json:"advisories"`
			Packages   []pkg `json:"packages"`
		}{res.Count(), []pkg{}}
		for _, p := range res.Packages {
			e := pkg{Name: p.Name, Version: p.Version, Query: p.Query, NotChecked: p.NotChecked, Ignored: p.Ignored, Vulns: []vuln{}}
			for _, x := range p.Vulns {
				e.Vulns = append(e.Vulns, vuln{x.ID, x.Aliases, x.Severity(), x.Title(), x.URL()})
			}
			out.Packages = append(out.Packages, e)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(io.Out, string(b))
	} else {
		printAudit(io, res)
	}
	if res.Count() > 0 {
		return exitVulnerable
	}
	return exitOK
}

func printAudit(io IO, res audit.Result) {
	affected, ignored, unchecked := 0, 0, 0
	for _, p := range res.Packages {
		ignored += p.Ignored
		if p.NotChecked != "" {
			unchecked++
			fmt.Fprintf(io.Err, "warning: %s %s was not checked: %s\n", p.Name, p.Version, p.NotChecked)
		}
		if len(p.Vulns) == 0 {
			continue
		}
		affected++
		fmt.Fprintf(io.Out, "%s %s: %d advisor%s\n", p.Name, p.Version, len(p.Vulns), plural(len(p.Vulns), "y", "ies"))
		for _, x := range p.Vulns {
			sev := x.Severity()
			if sev == "" {
				sev = "-"
			}
			fmt.Fprintf(io.Out, "  %-18s %-9s %s\n", x.ID, sev, x.Title())
			fmt.Fprintf(io.Out, "  %-18s %-9s %s\n", "", "", x.URL())
		}
		fmt.Fprintln(io.Out)
	}
	checked := len(res.Packages) - unchecked
	switch {
	case res.Count() == 0:
		fmt.Fprintf(io.Out, "No known vulnerabilities in %d locked package(s) (osv.dev).\n", checked)
	default:
		fmt.Fprintf(io.Out, "%d advisor%s in %d of %d locked package(s). A match means the locked version is in the range the advisory\nlists; whether your use of the library is affected is for you to judge. Accept an advisory with --ignore ID.\n",
			res.Count(), plural(res.Count(), "y", "ies"), affected, checked)
	}
	if ignored > 0 {
		fmt.Fprintf(io.Out, "%d advisor%s ignored.\n", ignored, plural(ignored, "y", "ies"))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

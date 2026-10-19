package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/pixelpolishers/kiln-cli/internal/install"
	"github.com/pixelpolishers/kiln-cli/internal/license"
)

// listFlag collects repeatable, comma separated values.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

// cmdLicenses reports the licenses of the locked packages and checks them against a policy.
func cmdLicenses(args []string, io IO) int {
	fs := newFlagSet("licenses", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln licenses [-d DIR] [--allow ID,...] [--deny ID,...] [--registry URL] [--json]")
		fmt.Fprintln(io.Err, "\nLists the license of every package in kiln.lock, direct and transitive, and changes nothing.")
		fmt.Fprintln(io.Err, "With --allow or --deny it also checks them and exits with status", exitLicense, "when one fails.")
		fmt.Fprintln(io.Err, "IDs are SPDX ids; GPL-3.0 also covers GPL-3.0-only, GPL-3.0-or-later and GPL-3.0+; a trailing * is a prefix")
		fmt.Fprintln(io.Err, "(AGPL-*). In 'A OR B' one acceptable side is enough; in 'A AND B' both must be. A package without a")
		fmt.Fprintln(io.Err, "valid license expression fails when a policy is given.")
		printFlags(fs, io.Err)
	}
	common := addCommon(fs)
	dir := fs.String("dir", ".", "project directory `DIR`")
	alias(fs, "d", "dir")
	var allow, deny listFlag
	fs.Var(&allow, "allow", "accept only these licenses `IDS` (comma separated, repeatable)")
	fs.Var(&deny, "deny", "refuse these licenses `IDS` (comma separated, repeatable)")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageErr(io, fs, "licenses takes no arguments")
	}
	env, _, err := installEnv(common.registry, "", "", io)
	if err != nil {
		return fail(io, err)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return fail(io, err)
	}
	res, err := install.Licenses(context.Background(), env, abs)
	if err != nil {
		return failLockRead(io, err)
	}
	if res.Stale {
		fmt.Fprintln(io.Err, "warning: kiln.json changed since kiln.lock was written; this reports the lock, run 'kiln update' to bring it up to date")
	}

	policy := license.Policy{Allow: allow, Deny: deny}
	type row struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		License string `json:"license"`
		Direct  bool   `json:"direct"`
		Verdict string `json:"verdict,omitempty"`
	}
	var rows []row
	failed := 0
	counts := map[string]int{}
	for _, p := range res.Packages {
		r := row{p.Name, p.Version, p.License, p.Direct, ""}
		if !policy.Empty() {
			v := policy.Check(p.License)
			r.Verdict = string(v)
			if v != license.OK {
				failed++
			}
		}
		lic := p.License
		if lic == "" {
			lic = "(none)"
		}
		counts[lic]++
		rows = append(rows, r)
	}

	exit := exitOK
	if failed > 0 {
		exit = exitLicense
	}
	if common.json {
		out := struct {
			Packages []row `json:"packages"`
			Failed   int   `json:"failed"`
		}{rows, failed}
		if out.Packages == nil {
			out.Packages = []row{}
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(io.Out, string(b))
		return exit
	}
	if len(rows) == 0 {
		fmt.Fprintln(io.Out, "No packages in kiln.lock.")
		return exitOK
	}
	tw := tabwriter.NewWriter(io.Out, 0, 4, 2, ' ', 0)
	header := "NAME\tVERSION\tLICENSE\t"
	if !policy.Empty() {
		header += "POLICY\t"
	}
	fmt.Fprintln(tw, header+"")
	for _, r := range rows {
		lic := r.License
		if lic == "" {
			lic = "(none)"
		}
		name := r.Name
		if !r.Direct {
			name += " (indirect)"
		}
		line := fmt.Sprintf("%s\t%s\t%s\t", name, r.Version, lic)
		if !policy.Empty() {
			line += r.Verdict + "\t"
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()

	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	fmt.Fprintf(io.Out, "\n%d package(s): %s\n", len(rows), strings.Join(parts, ", "))
	if failed > 0 {
		fmt.Fprintf(io.Out, "%d package(s) break the license policy.\n", failed)
	}
	return exit
}

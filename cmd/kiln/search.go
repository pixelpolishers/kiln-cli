package main

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/pixelpolishers/kiln-cli/internal/client"
)

func cmdSearch(args []string, io IO) int {
	fs := newFlagSet("search", io)
	fs.Usage = func() { fmt.Fprintln(io.Err, "Usage: kiln search [query] [--registry URL] [--json]") }
	common := addCommon(fs)
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 1 {
		return usageErr(io, fs, "search takes at most one query (quote it if it has spaces)")
	}
	query := ""
	if len(pos) == 1 {
		query = pos[0]
	}

	results, err := client.New(common.registry).Search(context.Background(), query)
	if err != nil {
		return fail(io, err)
	}
	if common.json {
		enc := json.NewEncoder(io.Out)
		enc.SetIndent("", "  ")
		enc.Encode(results)
		return exitOK
	}
	if len(results) == 0 {
		fmt.Fprintln(io.Err, "no packages found")
		return exitOK
	}
	tw := tabwriter.NewWriter(io.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLATEST\tLICENSE\tDESCRIPTION")
	for _, p := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Latest, p.License, p.Description)
	}
	tw.Flush()
	return exitOK
}

package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pixelpolishers/kiln-cli/internal/cache"
)

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// cmdCache lists and clears the shared cache.
func cmdCache(args []string, io IO) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "-help" {
		fmt.Fprintln(io.Err, "Usage: kiln cache <ls|clear> [options]")
		fmt.Fprintln(io.Err, "\n  ls      list what the cache holds and how much space it takes")
		fmt.Fprintln(io.Err, "  clear   delete cached builds, source checkouts and logs")
		fmt.Fprintln(io.Err, "\nRun 'kiln cache <command> -h' for the options.")
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	switch args[0] {
	case "ls":
		return cmdCacheLs(args[1:], io)
	case "clear":
		return cmdCacheClear(args[1:], io)
	}
	fmt.Fprintf(io.Err, "kiln: unknown cache command %q (use ls or clear)\n", args[0])
	return exitUsage
}

func cmdCacheLs(args []string, io IO) int {
	fs := newFlagSet("cache ls", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln cache ls")
		fmt.Fprintln(io.Err, "\nLists the built libraries, source checkouts and logs in the shared cache ($KILN_CACHE_DIR, default the user cache directory).")
		printFlags(fs, io.Err)
	}
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageErr(io, fs, "cache ls takes no arguments")
	}
	c, err := cache.Default()
	if err != nil {
		return fail(io, err)
	}
	items, err := c.List(cache.Parts...)
	if err != nil {
		return fail(io, err)
	}
	fmt.Fprintf(io.Out, "cache: %s\n", c.Root)
	if len(items) == 0 {
		fmt.Fprintln(io.Out, "empty")
		return exitOK
	}
	tw := tabwriter.NewWriter(io.Out, 0, 4, 2, ' ', 0)
	var total int64
	count := map[cache.Part]int{}
	size := map[cache.Part]int64{}
	for _, it := range items {
		if it.Part != cache.Logs {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", it.Part, it.Name, humanSize(it.Size))
		}
		count[it.Part]++
		size[it.Part] += it.Size
		total += it.Size
	}
	tw.Flush()
	fmt.Fprintln(io.Out)
	for _, p := range cache.Parts {
		fmt.Fprintf(io.Out, "%-9s %4d  %s\n", p, count[p], humanSize(size[p]))
	}
	fmt.Fprintf(io.Out, "%-9s       %s\n", "total", humanSize(total))
	return exitOK
}

func cmdCacheClear(args []string, io IO) int {
	fs := newFlagSet("cache clear", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln cache clear [--packages] [--sources] [--logs] [--dry-run]")
		fmt.Fprintln(io.Err, "\nDeletes the contents of the shared cache. Without options it deletes everything: built libraries, source")
		fmt.Fprintln(io.Err, "checkouts, logs and abandoned temporary directories. It never touches projects, kiln.lock, a local repository")
		fmt.Fprintln(io.Err, "(--local) or the library sources under vendor/.")
		fmt.Fprintln(io.Err, "\nProjects that were installed keep their kiln.cmake, which points into the cache: they cannot be built")
		fmt.Fprintln(io.Err, "until 'kiln install' has made the libraries again (from the lock, so with the same versions).")
		fmt.Fprintln(io.Err, "Do not run it while another kiln is installing.")
		printFlags(fs, io.Err)
	}
	packages := fs.Bool("packages", false, "delete only the built libraries")
	sources := fs.Bool("sources", false, "delete only the source checkouts")
	logs := fs.Bool("logs", false, "delete only the build logs")
	dry := fs.Bool("dry-run", false, "show what would be deleted and delete nothing")
	alias(fs, "n", "dry-run")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageErr(io, fs, "cache clear takes no arguments")
	}
	var parts []cache.Part
	if *packages {
		parts = append(parts, cache.Packages)
	}
	if *sources {
		parts = append(parts, cache.Sources)
	}
	if *logs {
		parts = append(parts, cache.Logs)
	}
	all := len(parts) == 0
	if all {
		parts = cache.Parts
	}
	c, err := cache.Default()
	if err != nil {
		return fail(io, err)
	}
	items, err := c.List(parts...)
	if err != nil {
		return fail(io, err)
	}
	if all {
		stale, err := c.Stale(time.Now())
		if err != nil {
			return fail(io, err)
		}
		items = append(items, stale...)
	}
	verb := "Deleted"
	if *dry {
		verb = "Would delete"
	}
	var total int64
	count := map[cache.Part]int{}
	var failures []string
	for _, it := range items {
		if !*dry {
			if err := c.Remove(it.Path); err != nil {
				failures = append(failures, err.Error())
				continue
			}
		}
		count[it.Part]++
		total += it.Size
	}
	if !*dry {
		c.Prune(parts...)
	}
	var what []string
	for _, p := range cache.Parts {
		if count[p] > 0 {
			what = append(what, fmt.Sprintf("%d %s", count[p], p))
		}
	}
	if count[""] > 0 {
		what = append(what, fmt.Sprintf("%d abandoned temporary director%s", count[""], map[bool]string{true: "y", false: "ies"}[count[""] == 1]))
	}
	if len(what) == 0 {
		fmt.Fprintf(io.Out, "Nothing to delete in %s\n", c.Root)
	} else {
		fmt.Fprintf(io.Out, "%s %s from %s, freeing %s\n", verb, strings.Join(what, ", "), c.Root, humanSize(total))
	}
	if len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintf(io.Err, "kiln: %s\n", f)
		}
		return exitError
	}
	if !*dry && len(what) > 0 && (all || *packages) {
		fmt.Fprintln(io.Out, "Projects need 'kiln install' again before they can be built.")
	}
	return exitOK
}

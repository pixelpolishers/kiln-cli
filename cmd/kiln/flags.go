package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// alias registers short as a second name for the flag already defined as long,
// so "-s" and "--setting" set the same value. (Go's flag package accepts one
// or two dashes for every flag; the help output shows the GNU style: single
// letters with one dash, longer names with two.)
func alias(fs *flag.FlagSet, short, long string) {
	f := fs.Lookup(long)
	if f == nil {
		panic("alias: no flag " + long)
	}
	fs.Var(f.Value, short, f.Usage)
}

// printFlags prints the flags of fs, grouping aliases of the same flag:
//
//	-s, --setting key=value   override a build setting (repeatable)
//	    --binary-only         never build from source
func printFlags(fs *flag.FlagSet, w io.Writer) {
	type entry struct {
		names []string
		flag  *flag.Flag
	}
	var entries []*entry
	byValue := map[flag.Value]*entry{}
	fs.VisitAll(func(f *flag.Flag) {
		e, ok := byValue[f.Value]
		if !ok {
			e = &entry{flag: f}
			byValue[f.Value] = e
			entries = append(entries, e)
		}
		e.names = append(e.names, f.Name)
	})

	type row struct{ left, right string }
	var rows []row
	width := 0
	for _, e := range entries {
		sort.Slice(e.names, func(i, j int) bool {
			if (len(e.names[i]) == 1) != (len(e.names[j]) == 1) {
				return len(e.names[i]) == 1 // short names first
			}
			return e.names[i] < e.names[j]
		})
		var short, long []string
		for _, n := range e.names {
			if len(n) == 1 {
				short = append(short, "-"+n)
			} else {
				long = append(long, "--"+n)
			}
		}
		placeholder, usage := flag.UnquoteUsage(e.flag)
		left := strings.Join(append(short, long...), ", ")
		if len(short) == 0 {
			left = "    " + left
		}
		if placeholder != "" {
			left += " " + placeholder
		}
		if d := e.flag.DefValue; d != "" && d != "0" && d != "false" && !strings.Contains(usage, "default") {
			usage += fmt.Sprintf(" (default %s)", d)
		}
		rows = append(rows, row{left, usage})
		if len(left) > width {
			width = len(left)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.TrimLeft(rows[i].left, " -") < strings.TrimLeft(rows[j].left, " -")
	})
	fmt.Fprintln(w, "\nOptions:")
	for _, r := range rows {
		fmt.Fprintf(w, "  %-*s  %s\n", width, r.left, r.right)
	}
}

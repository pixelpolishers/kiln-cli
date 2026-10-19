package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/pixelpolishers/kiln-cli/internal/settings"
	"github.com/pixelpolishers/kiln-core/manifest"
)

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func cmdSettings(args []string, io IO) int {
	fs := newFlagSet("settings", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln settings [-s key=value]... [--json]")
		fmt.Fprintln(io.Err, "\nKeys: "+strings.Join(settings.Keys, ", "))
		printFlags(fs, io.Err)
	}
	var overrides stringList
	fs.Var(&overrides, "setting", "override a setting `key=value`, e.g. -s build_type=debug (repeatable)")
	alias(fs, "s", "setting")
	asJSON := fs.Bool("json", false, "print the settings object as JSON")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageErr(io, fs, "settings takes no arguments")
	}

	res := settings.Detect(settings.SystemEnv())
	if err := res.Settings.Apply(overrides, res.Sources); err != nil {
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitUsage
	}
	if res.Settings.Compiler.Name == "" || res.Settings.Stdlib == "" {
		for _, n := range res.Notes {
			fmt.Fprintf(io.Err, "kiln: %s\n", n)
		}
		return fail(io, fmt.Errorf("cannot determine the compiler and standard library; set them with -s compiler.name=, -s compiler.version= and -s stdlib="))
	}

	raw, err := json.Marshal(res.Settings)
	if err != nil {
		return fail(io, err)
	}
	validator, err := manifest.NewValidator()
	if err != nil {
		return fail(io, err)
	}
	if err := validator.ValidateSettings(raw); err != nil {
		fmt.Fprintf(io.Err, "kiln: the resulting settings are invalid:\n%v\n", err)
		return exitUsage
	}

	if *asJSON {
		enc := json.NewEncoder(io.Out)
		enc.SetIndent("", "  ")
		enc.Encode(res.Settings)
		return exitOK
	}
	tw := tabwriter.NewWriter(io.Out, 0, 0, 2, ' ', 0)
	for _, k := range settings.Keys {
		v := res.Settings.Value(k)
		if v == "" {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", k, v, res.Sources[k])
	}
	tw.Flush()
	if res.Compiler != "" {
		fmt.Fprintf(io.Err, "\ncompiler program: %s\n", res.Compiler)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(io.Err, "note: %s\n", n)
	}
	return exitOK
}

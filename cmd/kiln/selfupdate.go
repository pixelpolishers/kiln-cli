package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/selfupdate"
	"github.com/pixelpolishers/kiln-core/semver"
)

// exitUpdate is returned by `self-update --check` when a newer version exists.
const exitUpdate = 11

// executablePath is the file self-update replaces; tests point it elsewhere.
var executablePath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// cmdSelfUpdate replaces the running kiln by the newest release.
func cmdSelfUpdate(args []string, io IO) int {
	fs := newFlagSet("self-update", io)
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln self-update [--check] [--pre] [--version VERSION] [--force]")
		fmt.Fprintln(io.Err, "\nLooks for a newer release of kiln, downloads it, checks its SHA-256 checksum against the one the release lists")
		fmt.Fprintln(io.Err, "and replaces this executable. Nothing is changed when the check fails. With --check it only reports: exit status 0 when")
		fmt.Fprintln(io.Err, "kiln is up to date, 11 when a newer version exists. Pre-releases are skipped unless this kiln is one or --pre is given.")
		printFlags(fs, io.Err)
	}
	check := fs.Bool("check", false, "only report whether a newer version exists, change nothing")
	pre := fs.Bool("pre", false, "also consider pre-releases (release candidates)")
	want := fs.String("version", "", "install this `VERSION` (for example v0.1.0) instead of the newest; may also be older")
	force := fs.Bool("force", false, "install even if this is not an older version, or a development build")
	url := fs.String("url", os.Getenv("KILN_UPDATE_URL"), "repository `URL` of the releases (default $KILN_UPDATE_URL or "+selfupdate.DefaultBaseURL+")")
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageErr(io, fs, "self-update takes no arguments")
	}

	exe, err := executablePath()
	if err != nil {
		return fail(io, err)
	}
	selfupdate.CleanUp(exe)

	cur, curErr := semver.Parse(strings.TrimPrefix(version, "v"))
	c := selfupdate.New(*url)
	ctx := context.Background()

	target := ""
	if *want != "" {
		target = "v" + strings.TrimPrefix(*want, "v")
		if _, err := semver.Parse(strings.TrimPrefix(target, "v")); err != nil {
			return usageErr(io, fs, fmt.Sprintf("%q is not a version", *want))
		}
	} else {
		tags, err := c.Tags(ctx)
		if err != nil {
			return failSelfUpdate(io, err)
		}
		target = selfupdate.Newest(tags, *pre || (curErr == nil && cur.IsPrerelease()))
		if target == "" {
			fmt.Fprintln(io.Err, "kiln: no release found (none published yet, or only pre-releases: try --pre)")
			return exitError
		}
	}
	tv, _ := semver.Parse(strings.TrimPrefix(target, "v"))

	switch {
	case curErr != nil && *want == "" && !*force:
		fmt.Fprintf(io.Err, "kiln: this is a development build (version %q), so it cannot tell whether it is outdated.\n", version)
		fmt.Fprintf(io.Err, "The newest release is %s; install it with: kiln self-update --force\n", target)
		return exitError
	case curErr == nil && semver.Compare(tv, cur) <= 0 && !*force:
		if *want != "" && semver.Compare(tv, cur) < 0 {
			break // asked for an older version on purpose
		}
		fmt.Fprintf(io.Out, "kiln %s is up to date (newest release: %s)\n", version, target)
		return exitOK
	}
	if *check {
		fmt.Fprintf(io.Out, "A newer version is available: %s -> %s (run 'kiln self-update')\n", version, target)
		return exitUpdate
	}

	fmt.Fprintf(io.Out, "Downloading kiln %s for %s/%s ...\n", target, runtime.GOOS, runtime.GOARCH)
	program, err := c.Fetch(ctx, target, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return failSelfUpdate(io, err)
	}
	fmt.Fprintln(io.Out, "Checksum OK.")
	if err := selfupdate.Replace(exe, program); err != nil {
		return fail(io, err)
	}
	fmt.Fprintf(io.Out, "Updated kiln %s -> %s (%s)\n", version, target, exe)
	return exitOK
}

func failSelfUpdate(io IO, err error) int {
	fmt.Fprintf(io.Err, "kiln: %v\n", err)
	var ne *selfupdate.NetworkError
	var ve *selfupdate.VerifyError
	switch {
	case errors.As(err, &ne):
		return exitNetwork
	case errors.As(err, &ve):
		return exitVerify
	}
	return exitError
}

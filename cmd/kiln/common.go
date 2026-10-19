package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/cache"
	"github.com/pixelpolishers/kiln-cli/internal/client"
	"github.com/pixelpolishers/kiln-cli/internal/install"
	"github.com/pixelpolishers/kiln-cli/internal/localrepo"
	"github.com/pixelpolishers/kiln-cli/internal/lock"
	"github.com/pixelpolishers/kiln-cli/internal/plan"
	"github.com/pixelpolishers/kiln-cli/internal/registrysrc"
	"github.com/pixelpolishers/kiln-cli/internal/settings"
	"github.com/pixelpolishers/kiln-cli/internal/sysreq"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/options"
	"github.com/pixelpolishers/kiln-core/solver"
)

// loadSettings detects the build settings, applies -s overrides and validates
// the result. On failure it reports the problem and returns an exit code.
func loadSettings(io IO, overrides []string) (settings.Settings, settings.Result, int) {
	res := settings.Detect(settings.SystemEnv())
	detected := res
	if err := res.Settings.Apply(overrides, res.Sources); err != nil {
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return settings.Settings{}, detected, exitUsage
	}
	if res.Settings.Compiler.Name == "" || res.Settings.Stdlib == "" {
		for _, n := range res.Notes {
			fmt.Fprintf(io.Err, "kiln: %s\n", n)
		}
		return settings.Settings{}, detected, fail(io, fmt.Errorf("cannot determine the compiler and standard library; set them with -s compiler.name=, -s compiler.version= and -s stdlib="))
	}
	raw, err := jsonMarshal(res.Settings)
	if err != nil {
		return settings.Settings{}, detected, fail(io, err)
	}
	v, err := manifest.NewValidator()
	if err != nil {
		return settings.Settings{}, detected, fail(io, err)
	}
	if err := v.ValidateSettings(raw); err != nil {
		fmt.Fprintf(io.Err, "kiln: the resulting settings are invalid:\n%v\n", err)
		return settings.Settings{}, detected, exitUsage
	}
	return res.Settings, detected, -1
}

// parseOptionFlags turns "-o vendor/pkg:option=value" flags into root requests.
func parseOptionFlags(flags []string) ([]options.Request, error) {
	var out []options.Request
	for _, f := range flags {
		target, value, ok := strings.Cut(f, "=")
		pkg, opt, ok2 := strings.Cut(target, ":")
		if !ok || !ok2 || pkg == "" || opt == "" || value == "" {
			return nil, fmt.Errorf("invalid option %q: want vendor/pkg:option=value", f)
		}
		out = append(out, options.Request{Package: pkg, Option: opt, Raw: value, From: "-o " + f, Root: true})
	}
	return out, nil
}

// installEnv assembles the services install and package need.
func installEnv(registryURL, localDir string, osName string, io IO) (install.Env, *manifest.Validator, error) {
	v, err := manifest.NewValidator()
	if err != nil {
		return install.Env{}, nil, err
	}
	c, err := cache.Default()
	if err != nil {
		return install.Env{}, nil, err
	}
	env := install.Env{
		Source:    &registrysrc.Source{Client: client.New(registryURL), OS: osName},
		Cache:     c,
		Validator: v,
		Progress:  io.Out,
	}
	if localDir != "" {
		env.Local = &localrepo.Repo{Root: localDir, Validator: v}
	}
	return env, v, nil
}

// failInstall reports an error from install/package and maps it to an exit code.
func failInstall(io IO, err error) int {
	var (
		conflict    *solver.ConflictError
		nobin       *install.NoBinaryError
		srcErr      *install.SourceBuildError
		missing     *install.MissingRequirementsError
		stale       *install.StaleLockError
		noLock      *install.NoLockError
		notLocked   *install.NotLockedError
		unavailable *install.LockedUnavailableError
		badLock     *install.InvalidLockError
		moved       *install.CommitChangedError
		verify      *install.VerifyError
		optErr      *plan.OptionsError
	)
	switch {
	case errors.As(err, &optErr):
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitResolution
	case errors.As(err, &conflict):
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitResolution
	case errors.As(err, &stale):
		fmt.Fprintf(io.Err, "kiln: kiln.lock is out of date: the dependencies in kiln.json changed since it was written.\n  Run `kiln update` to resolve them again and rewrite the lock.\n")
		return exitResolution
	case errors.As(err, &noLock):
		fmt.Fprintf(io.Err, "kiln: --locked: there is no kiln.lock.\n  Run `kiln install` once and commit the file it creates.\n")
		return exitResolution
	case errors.As(err, &notLocked):
		fmt.Fprintf(io.Err, "kiln: %v.\n  It is new in kiln.json, or applies only to another platform than the one the lock was written on.\n  Run `kiln update` to add it to the lock.\n", err)
		return exitResolution
	case errors.As(err, &unavailable):
		fmt.Fprintf(io.Err, "kiln: %v.\n  Run `kiln update %s` to move to a version that exists.\n", err, unavailable.Package)
		return exitResolution
	case errors.As(err, &badLock) && errors.Is(badLock.Err, lock.ErrOldFormat):
		fmt.Fprintf(io.Err, "kiln: %s was written by an older kiln and also contained machine-specific data that is no longer stored.\n  Run `kiln update` to rewrite it (versions are re-resolved).\n", badLock.Path)
		return exitError
	case errors.As(err, &badLock):
		fmt.Fprintf(io.Err, "kiln: %v\n  Fix or delete the file, then run `kiln update`.\n", err)
		return exitError
	case errors.As(err, &moved):
		fmt.Fprintf(io.Err, "kiln: %v.\n  Refusing to install. If the new commit is expected, run `kiln update %s`.\n", err, moved.Package)
		return exitVerify
	case errors.As(err, &missing):
		fmt.Fprintf(io.Err, "kiln: the system does not meet the requirements of the packages\n\n%s\nNothing was installed. Install the missing items and run the command again", sysreq.Format(missing.Failures))
		buildOnly := false
		for _, f := range missing.Failures {
			buildOnly = buildOnly || !f.Use
		}
		if buildOnly {
			fmt.Fprintf(io.Err, " (items that are only needed to build go away with --binary-only, which forbids source builds)")
		}
		fmt.Fprintln(io.Err, ".")
		return exitMissingReq
	case errors.As(err, &srcErr):
		fmt.Fprintf(io.Err, "kiln: building %s %s from source failed\n", srcErr.Package.Name, srcErr.Package.Version)
		fmt.Fprintf(io.Err, "  id: %s\n  %v\n", srcErr.Package.ID, srcErr.Err)
		return exitNoBinary
	case errors.As(err, &nobin):
		fmt.Fprintf(io.Err, "kiln: no binary available for %s %s\n", nobin.Package.Name, nobin.Package.Version)
		fmt.Fprintf(io.Err, "  id:     %s\n  looked: %s\n", nobin.Package.ID, strings.Join(nobin.Tried, ", "))
		if nobin.SourceDisabled {
			fmt.Fprintf(io.Err, "  Building from source is turned off (--binary-only). Publish a binary for these settings\n  (kiln package + publish + finalize), or allow source builds.\n")
		}
		return exitNoBinary
	case errors.As(err, &verify):
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitVerify
	}
	return fail(io, err)
}

func localFlag(fs interface {
	StringVar(*string, string, string, string)
}, dst *string) {
	fs.StringVar(dst, "local", os.Getenv("KILN_LOCAL_REPO"), "local binary repository `DIR` (default $KILN_LOCAL_REPO)")
}

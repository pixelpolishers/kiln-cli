// Command kiln is the C++ package manager command line tool.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/client"
)

var version = "dev"

// Exit codes (see docs/cli.md).
const (
	exitOK         = 0
	exitError      = 1
	exitUsage      = 2
	exitResolution = 3
	exitNoBinary   = 4
	exitVerify     = 5
	exitMissingReq = 7
	exitOutdated   = 8
	exitVulnerable = 9
	exitLicense    = 10
	exitNetwork    = 6
	usageSummary   = `kiln - C++ package manager (proof of concept)

Usage: kiln <command> [options]

Commands:
  search [query]          search the registry
  info <vendor/pkg>[:<version>]
                          show a package
  resolve <vendor/pkg>[:<constraint>]...
                          show which versions would be installed
  settings                show the detected build settings
  init [vendor/name]      create a kiln.json
  require <vendor/pkg>[:<constraint>]...
                          add packages to kiln.json and install them
  remove <vendor/pkg>...  remove packages from kiln.json
  install                 install the dependencies of ./kiln.json (follows kiln.lock)
  update [pkg...]         resolve again and rewrite kiln.lock
  outdated                list locked packages that have newer versions
  audit                   check the locked packages for known vulnerabilities (osv.dev)
  cache <ls|clear>        list or delete the shared cache of builds and sources
  licenses                list the licenses of the locked packages, optionally check a policy
  why <vendor/pkg>        explain why a package is installed
  why-not <vendor/pkg>[:<constraint>]
                          explain what keeps a package (version) out
  package                 pack a built tree into a package archive
  publish                 add packed archives to a local repository
  finalize                write the asset manifest of a local repository
  version                 print the kiln version
  self-update             replace this kiln by the newest release

Run 'kiln <command> -h' for the options of a command.

Environment:
  KILN_REGISTRY           registry URL (default ` + client.DefaultRegistry + `)
  KILN_LOCAL_REPO         local binary repository (see 'kiln install -h')
  KILN_CACHE_DIR          package cache (default: the user cache directory)
`
)

// IO bundles the streams of a command so tests can capture output.
type IO struct {
	Out, Err io.Writer
}

func main() {
	os.Exit(run(os.Args[1:], IO{Out: os.Stdout, Err: os.Stderr}))
}

func run(args []string, io IO) int {
	if len(args) == 0 {
		fmt.Fprint(io.Err, usageSummary)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "search":
		return cmdSearch(rest, io)
	case "info":
		return cmdInfo(rest, io)
	case "resolve":
		return cmdResolve(rest, io)
	case "settings":
		return cmdSettings(rest, io)
	case "init":
		return cmdInit(rest, io)
	case "require":
		return cmdRequire(rest, io)
	case "remove":
		return cmdRemove(rest, io)
	case "install":
		return cmdInstall(rest, io)
	case "update":
		return cmdUpdate(rest, io)
	case "outdated":
		return cmdOutdated(rest, io)
	case "audit":
		return cmdAudit(rest, io)
	case "cache":
		return cmdCache(rest, io)
	case "licenses":
		return cmdLicenses(rest, io)
	case "why":
		return cmdWhy(rest, io)
	case "why-not":
		return cmdWhyNot(rest, io)
	case "package":
		return cmdPackage(rest, io)
	case "publish":
		return cmdPublish(rest, io)
	case "finalize":
		return cmdFinalize(rest, io)
	case "self-update":
		return cmdSelfUpdate(rest, io)
	case "version", "--version":
		fmt.Fprintln(io.Out, "kiln", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(io.Out, usageSummary)
		return exitOK
	}
	fmt.Fprintf(io.Err, "kiln: unknown command %q\n\n%s", cmd, usageSummary)
	return exitUsage
}

// newFlagSet creates a flag set that reports errors to io.Err and does not
// exit, so run can translate the outcome into an exit code.
func newFlagSet(name string, io IO) *flag.FlagSet {
	fs := flag.NewFlagSet("kiln "+name, flag.ContinueOnError)
	fs.SetOutput(io.Err)
	return fs
}

// parse parses args, allowing flags before and after positional arguments.
// It returns the positional arguments and an exit code (-1 to continue).
func parse(fs *flag.FlagSet, args []string) ([]string, int) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, exitOK
			}
			return nil, exitUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, -1
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

type commonFlags struct {
	registry string
	json     bool
}

func addCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.registry, "registry", os.Getenv("KILN_REGISTRY"), "registry `URL` (default $KILN_REGISTRY or "+client.DefaultRegistry+")")
	fs.BoolVar(&c.json, "json", false, "machine-readable output")
	return c
}

// fail prints err and maps it to an exit code.
func fail(io IO, err error) int {
	fmt.Fprintf(io.Err, "kiln: %v\n", err)
	var ne *client.NetworkError
	if errors.As(err, &ne) {
		return exitNetwork
	}
	return exitError
}

func usageErr(io IO, fs *flag.FlagSet, msg string) int {
	fmt.Fprintf(io.Err, "kiln: %s\n", msg)
	fs.Usage()
	return exitUsage
}

// splitNameVersion splits "vendor/pkg:1.2.3" into its parts.
func splitNameVersion(arg string) (name, version string) {
	name, version, _ = strings.Cut(arg, ":")
	return
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

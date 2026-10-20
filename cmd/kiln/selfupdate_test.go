package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-cli/internal/selfupdate/fakerelease"
)

// selfUpdate runs `kiln self-update` as the given version, against a fake release server,
// replacing a stand-in executable. It returns the exit code, the output and the program file.
func selfUpdate(t *testing.T, current string, server string, args ...string) (int, string, string) {
	t.Helper()
	old := version
	version = current
	oldPath := executablePath
	exe := filepath.Join(t.TempDir(), "kiln")
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { version, executablePath = old, oldPath })
	var out, errb bytes.Buffer
	code := run(append([]string{"self-update", "--url", server}, args...), IO{Out: &out, Err: &errb})
	return code, out.String() + errb.String(), exe
}

func contents(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSelfUpdateReplacesTheExecutableWithTheNewestStableRelease(t *testing.T) {
	srv := fakerelease.Server(t,
		fakerelease.Release{Tag: "v0.3.0-rc1", Program: "RC"},
		fakerelease.Release{Tag: "v0.2.0", Program: "NEW"},
		fakerelease.Release{Tag: "v0.1.0", Program: "OLDER"})
	code, out, exe := selfUpdate(t, "0.1.0", srv.URL)
	if code != exitOK || !strings.Contains(out, "Updated kiln 0.1.0 -> v0.2.0") || contents(t, exe) != "NEW" {
		t.Errorf("exit %d, file %q, output:\n%s", code, contents(t, exe), out)
	}
}

func TestSelfUpdateCheckReportsWithoutChangingAnything(t *testing.T) {
	srv := fakerelease.Server(t, fakerelease.Release{Tag: "v0.2.0", Program: "NEW"})
	code, out, exe := selfUpdate(t, "0.1.0", srv.URL, "--check")
	if code != exitUpdate || !strings.Contains(out, "0.1.0 -> v0.2.0") || contents(t, exe) != "OLD" {
		t.Errorf("exit %d (want %d), file %q, output:\n%s", code, exitUpdate, contents(t, exe), out)
	}
	code, out, _ = selfUpdate(t, "0.2.0", srv.URL, "--check")
	if code != exitOK || !strings.Contains(out, "up to date") {
		t.Errorf("exit %d, output:\n%s", code, out)
	}
	code, out, exe = selfUpdate(t, "0.2.0", srv.URL)
	if code != exitOK || !strings.Contains(out, "up to date") || contents(t, exe) != "OLD" {
		t.Errorf("an up-to-date kiln must not be replaced: exit %d, file %q\n%s", code, contents(t, exe), out)
	}
}

func TestSelfUpdatePreReleases(t *testing.T) {
	srv := fakerelease.Server(t, fakerelease.Release{Tag: "v0.2.0-rc1", Program: "RC1"}, fakerelease.Release{Tag: "v0.1.0", Program: "STABLE"})
	// a stable kiln ignores the release candidate of the next version ...
	if code, out, _ := selfUpdate(t, "0.1.0", srv.URL); code != exitOK || !strings.Contains(out, "up to date") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// ... unless asked
	if code, _, exe := selfUpdate(t, "0.1.0", srv.URL, "--pre"); code != exitOK || contents(t, exe) != "RC1" {
		t.Errorf("--pre: exit %d, file %q", code, contents(t, exe))
	}
	// a release candidate follows the release candidates by itself
	srv2 := fakerelease.Server(t, fakerelease.Release{Tag: "v0.1.0-rc2", Program: "RC2"}, fakerelease.Release{Tag: "v0.1.0-rc1", Program: "RC1"})
	if code, _, exe := selfUpdate(t, "0.1.0-rc1", srv2.URL); code != exitOK || contents(t, exe) != "RC2" {
		t.Errorf("rc1 -> rc2: exit %d, file %q", code, contents(t, exe))
	}
	// nothing but release candidates exist: a stable-only request finds nothing and says why
	srv3 := fakerelease.Server(t, fakerelease.Release{Tag: "v0.1.0-rc1", Program: "RC1"})
	if code, out, _ := selfUpdate(t, "dev", srv3.URL, "--force"); code != exitError || !strings.Contains(out, "--pre") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestSelfUpdateOfADevelopmentBuildNeedsForce(t *testing.T) {
	srv := fakerelease.Server(t, fakerelease.Release{Tag: "v0.2.0", Program: "NEW"})
	code, out, exe := selfUpdate(t, "dev", srv.URL)
	if code != exitError || !strings.Contains(out, "development build") || contents(t, exe) != "OLD" {
		t.Errorf("exit %d, file %q\n%s", code, contents(t, exe), out)
	}
	if code, _, exe := selfUpdate(t, "dev-abc1234", srv.URL, "--force"); code != exitOK || contents(t, exe) != "NEW" {
		t.Errorf("--force: exit %d, file %q", code, contents(t, exe))
	}
}

func TestSelfUpdateVersionCanDowngrade(t *testing.T) {
	srv := fakerelease.Server(t, fakerelease.Release{Tag: "v0.2.0", Program: "NEW"}, fakerelease.Release{Tag: "v0.1.0", Program: "OLDER"})
	code, out, exe := selfUpdate(t, "0.2.0", srv.URL, "--version", "0.1.0")
	if code != exitOK || contents(t, exe) != "OLDER" || !strings.Contains(out, "0.2.0 -> v0.1.0") {
		t.Errorf("exit %d, file %q\n%s", code, contents(t, exe), out)
	}
	if code, _, _ := selfUpdate(t, "0.2.0", srv.URL, "--version", "banana"); code != exitUsage {
		t.Errorf("a version that is not a version: exit %d, want %d", code, exitUsage)
	}
}

func TestSelfUpdateLeavesTheExecutableAloneWhenSomethingIsWrong(t *testing.T) {
	bad := fakerelease.Server(t, fakerelease.Release{Tag: "v0.2.0", Program: "TAMPERED", BadSum: true})
	code, out, exe := selfUpdate(t, "0.1.0", bad.URL)
	if code != exitVerify || !strings.Contains(out, "checksum mismatch") || contents(t, exe) != "OLD" {
		t.Errorf("exit %d (want %d), file %q\n%s", code, exitVerify, contents(t, exe), out)
	}
	code, out, exe = selfUpdate(t, "0.1.0", "http://127.0.0.1:1")
	if code != exitNetwork || contents(t, exe) != "OLD" {
		t.Errorf("no network: exit %d (want %d), file %q\n%s", code, exitNetwork, contents(t, exe), out)
	}
}

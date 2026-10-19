package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// licenseEnv: the project requires v/app (MIT), which requires v/gpl (GPL-3.0-only);
// v/dual (GPL-3.0-only OR Apache-2.0) and v/bare (no license) are required directly.
func licenseEnv(t *testing.T) (*lockEnv, string) {
	t.Helper()
	e := newLockEnv(t)
	e.releaseWith("v/gpl", "1.0.0", `,"license":"GPL-3.0-only"`)
	e.releaseWith("v/dual", "1.0.0", `,"license":"GPL-3.0-only OR Apache-2.0"`)
	e.releaseWith("v/bare", "1.0.0", ``)
	e.releaseWith("v/app", "1.0.0", `,"license":"MIT","require":{"v/gpl":"^1.0"}`)
	project := e.project(`"v/app":"^1.0","v/dual":"^1.0","v/bare":"^1.0"`)
	mustRun(t, e.cmd("install", project)...)
	return e, project
}

func TestLicensesListsDirectAndIndirectPackages(t *testing.T) {
	e, project := licenseEnv(t)
	out := mustRun(t, explain(e, project, "licenses")...)
	for _, want := range []string{"v/app", "MIT", "v/gpl (indirect)", "GPL-3.0-only", "(none)", "4 package(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "POLICY") {
		t.Errorf("no policy was given:\n%s", out)
	}
}

func TestLicensesPolicy(t *testing.T) {
	e, project := licenseEnv(t)

	code, out, _ := runCLI(explain(e, project, "licenses", "--deny", "GPL-3.0")...)
	if code != exitLicense {
		t.Fatalf("exit %d, want %d:\n%s", code, exitLicense, out)
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "v/gpl"):
			if !strings.Contains(line, "denied") {
				t.Errorf("%q", line)
			}
		case strings.HasPrefix(line, "v/dual"), strings.HasPrefix(line, "v/app"):
			if !strings.Contains(line, "ok") {
				t.Errorf("the dual license has an acceptable side: %q", line)
			}
		case strings.HasPrefix(line, "v/bare"):
			if !strings.Contains(line, "unknown") {
				t.Errorf("%q", line)
			}
		}
	}

	code, out, _ = runCLI(explain(e, project, "licenses", "--allow", "MIT,Apache-2.0")...)
	if code != exitLicense || !strings.Contains(out, "not allowed") || !strings.Contains(out, "2 package(s) break") {
		t.Errorf("%d:\n%s", code, out)
	}

	code, _, _ = runCLI(explain(e, project, "licenses", "--deny", "AGPL-*")...)
	if code != exitLicense {
		t.Errorf("v/bare has no license, so a policy fails: %d", code)
	}

	var parsed struct {
		Failed   int
		Packages []struct{ Name, License, Verdict string }
	}
	_, js, _ := runCLI(explain(e, project, "licenses", "--deny", "GPL-3.0", "--json")...)
	if err := json.Unmarshal([]byte(js), &parsed); err != nil || parsed.Failed != 2 || len(parsed.Packages) != 4 {
		t.Errorf("%v %+v", err, parsed)
	}
}

func TestLicensesNeedsALock(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/solo", "1.0.0")
	project := e.project(`"v/solo":"^1.0"`)
	code, _, errs := runCLI(explain(e, project, "licenses")...)
	if code == exitOK || !strings.Contains(errs, "kiln.lock") {
		t.Errorf("%d %s", code, errs)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// whyEnv: the project requires v/app and v/lib directly; v/app requires v/lib ~1.0.0;
// v/solo is required directly only.
func whyEnv(t *testing.T) (*lockEnv, string) {
	t.Helper()
	e := newLockEnv(t)
	e.release("v/lib", "1.0.0")
	e.release("v/solo", "1.0.0")
	app := libManifest("v/app", `,"require":{"v/lib":"~1.0.0"}`)
	e.reg.add("v/app", "1.0.0", app)
	write(t, filepath.Join(e.work, "pkg", "v_app", "kiln.json"), app)
	publish(t, e.reg.srv.URL, filepath.Join(e.work, "pkg", "v_app"), "v/app", "1.0.0", e.repo, "app")
	project := e.project(`"v/app":"^1.0","v/lib":"^1.0","v/solo":"^1.0"`)
	mustRun(t, e.cmd("install", project)...)
	return e, project
}

func explain(e *lockEnv, project, verb string, extra ...string) []string {
	return append([]string{verb, "--registry", e.reg.srv.URL, "--dir", project}, extra...)
}

func TestWhyListsEveryWayAPackageIsNeeded(t *testing.T) {
	e, project := whyEnv(t)
	out := mustRun(t, explain(e, project, "why", "v/lib")...)
	for _, want := range []string{
		"v/lib 1.0.0 is installed because:",
		"kiln.json -> v/app ^1.0 (1.0.0) -> v/lib ~1.0.0 (1.0.0)",
		"kiln.json -> v/lib ^1.0 (1.0.0)   <- required directly",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "required directly") > strings.Index(out, "v/app ^1.0") {
		t.Errorf("the shortest explanation comes first:\n%s", out)
	}

	solo := mustRun(t, explain(e, project, "why", "v/solo")...)
	if strings.Count(solo, "->") != 1 {
		t.Errorf("%s", solo)
	}

	code, _, errs := runCLI(explain(e, project, "why", "v/nothing")...)
	if code != exitError || !strings.Contains(errs, "is not in kiln.lock") {
		t.Errorf("a package that is not installed: %d %s", code, errs)
	}
	if code, _, _ := runCLI(explain(e, project, "why")...); code != exitUsage {
		t.Errorf("why needs a package: %d", code)
	}

	var parsed struct {
		Direct bool
		Paths  [][]struct{ Name, Version, Constraint string }
	}
	if err := json.Unmarshal([]byte(mustRun(t, explain(e, project, "why", "v/lib", "--json")...)), &parsed); err != nil || !parsed.Direct || len(parsed.Paths) != 2 || parsed.Paths[1][0].Name != "v/app" {
		t.Errorf("%v %+v", err, parsed)
	}
}

func TestWhyNeedsALock(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/solo", "1.0.0")
	project := e.project(`"v/solo":"^1.0"`)
	code, _, errs := runCLI(explain(e, project, "why", "v/solo")...)
	if code == exitOK || !strings.Contains(errs, "kiln.lock") {
		t.Errorf("%d %s", code, errs)
	}
}

func TestWhyNotNamesEveryBlocker(t *testing.T) {
	e, project := whyEnv(t)
	e.reg.add("v/lib", "1.1.0", libManifest("v/lib", ""))
	e.reg.add("v/lib", "2.0.0", libManifest("v/lib", ""))

	// v/lib 1.1: kiln.json says ^1.0 (fine), but v/app 1.0.0 needs ~1.0.0
	code, out, _ := runCLI(explain(e, project, "why-not", "v/lib:1.1")...)
	if code != exitResolution {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(out, "v/app 1.0.0 requires v/lib ~1.0.0") || strings.Contains(out, "would allow it") {
		t.Errorf("%s", out)
	}
	if !strings.Contains(out, "No combination of versions works") {
		t.Errorf("no version of v/app admits 1.1, so nothing works:\n%s", out)
	}

	// v/lib 2: kiln.json blocks it too, and so does v/app
	_, out, _ = runCLI(explain(e, project, "why-not", "v/lib:2")...)
	if !strings.Contains(out, "kiln.json requires v/lib ^1.0") || !strings.Contains(out, "v/app 1.0.0 requires v/lib ~1.0.0") {
		t.Errorf("both blockers must be listed:\n%s", out)
	}

	// a new v/app that accepts 1.1: blocked today, possible with a different set of versions
	e.reg.add("v/app", "1.1.0", libManifest("v/app", `,"require":{"v/lib":"^1.0"}`))
	code, out, _ = runCLI(explain(e, project, "why-not", "v/lib:1.1")...)
	if code != exitResolution || !strings.Contains(out, "(v/app 1.1.0 would allow it)") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "v/app: 1.0.0 -> 1.1.0") || !strings.Contains(out, "v/lib: 1.0.0 -> 1.1.0") || !strings.Contains(out, "run 'kiln update'") {
		t.Errorf("%s", out)
	}
}

func TestWhyNotWhenNothingIsInTheWay(t *testing.T) {
	e, project := whyEnv(t)
	e.reg.add("v/solo", "1.2.0", libManifest("v/solo", ""))
	e.reg.add("v/solo", "2.0.0", libManifest("v/solo", ""))

	out := mustRun(t, explain(e, project, "why-not", "v/solo:^1.0")...)
	if !strings.Contains(out, "is already satisfied: 1.0.0 is locked") {
		t.Errorf("%s", out)
	}
	// ^1.1 would be newer than the lock and kiln.json allows it
	out = mustRun(t, explain(e, project, "why-not", "v/solo:^1.1")...)
	if !strings.Contains(out, "Nothing prevents v/solo ^1.1") || !strings.Contains(out, "v/solo: 1.0.0 -> 1.2.0") || !strings.Contains(out, "kiln require v/solo:^1.1") {
		t.Errorf("%s", out)
	}
	// 2.0.0 is blocked by kiln.json only
	_, out, _ = runCLI(explain(e, project, "why-not", "v/solo:2")...)
	if !strings.Contains(out, "kiln.json requires v/solo ^1.0") || strings.Contains(out, "requires v/solo ^1.0\n  v/") {
		t.Errorf("%s", out)
	}
	// no such version at all, and an unknown package
	code, out, _ := runCLI(explain(e, project, "why-not", "v/solo:9")...)
	if code != exitResolution || !strings.Contains(out, "No version of v/solo matches 9: the registry has 2.0.0, 1.2.0, 1.0.0") {
		t.Errorf("%d %s", code, out)
	}
	if code, _, errs := runCLI(explain(e, project, "why-not", "v/ghost")...); code == exitOK || !strings.Contains(errs, "v/ghost") {
		t.Errorf("%d %s", code, errs)
	}
	// nothing was changed
	if got := lockedVersions(t, project); got["v/solo"] != "1.0.0" {
		t.Errorf("why-not must not touch the lock: %v", got)
	}
}

// fakeOSV answers like osv.dev for a few known tags and commits.
type fakeOSV struct {
	srv     *httptest.Server
	mu      sync.Mutex
	queries []string
}

func newFakeOSV(t *testing.T, byKey map[string][]string, details map[string]string) *fakeOSV {
	t.Helper()
	f := &fakeOSV{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/querybatch", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Queries []struct {
				Commit  string
				Version string
				Package *struct{ Name, Ecosystem string }
			}
		}
		json.NewDecoder(r.Body).Decode(&in)
		var results []string
		f.mu.Lock()
		for _, q := range in.Queries {
			key := "commit:" + q.Commit
			if q.Commit == "" {
				key = "tag:" + q.Package.Name + "@" + q.Version
			}
			f.queries = append(f.queries, key)
			var ids []string
			for _, id := range byKey[key] {
				ids = append(ids, fmt.Sprintf(`{"id":%q}`, id))
			}
			results = append(results, `{"vulns":[`+strings.Join(ids, ",")+`]}`)
		}
		f.mu.Unlock()
		fmt.Fprintf(w, `{"results":[%s]}`, strings.Join(results, ","))
	})
	mux.HandleFunc("GET /v1/vulns/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := details[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(d))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestAuditReportsAdvisoriesForLockedVersions(t *testing.T) {
	e := newLockEnv(t)
	solo := "0123456789abcdef0123456789abcdef01234567"
	e.reg.commits["v/solo@1.0.0"] = solo // pinned: looked up by commit
	e.release("v/lib", "1.0.0")          // unpinned: looked up by repository and tag
	e.release("v/solo", "1.0.0")
	project := e.project(`"v/lib":"^1.0","v/solo":"^1.0"`)
	mustRun(t, e.cmd("install", project)...)

	osvdb := newFakeOSV(t, map[string][]string{
		"tag:https://example.com/v/lib@v1.0.0": {"CVE-2030-0001", "GHSA-aaaa-bbbb-cccc"}, // the same vulnerability twice
		"commit:" + solo:                       {"CVE-2030-0002"},
	}, map[string]string{
		"CVE-2030-0001":       `{"id":"CVE-2030-0001","summary":"Heap overflow in the parser","severity":[{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}]}`,
		"GHSA-aaaa-bbbb-cccc": `{"id":"GHSA-aaaa-bbbb-cccc","aliases":["CVE-2030-0001"],"summary":"Heap overflow in the parser"}`,
		"CVE-2030-0002":       `{"id":"CVE-2030-0002","details":"Details line one.\nMore.","database_specific":{"severity":"low"}}`,
	})
	args := func(extra ...string) []string {
		return append(explain(e, project, "audit", "--osv-url", osvdb.srv.URL), extra...)
	}

	code, out, errs := runCLI(args()...)
	if code != exitVulnerable {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitVulnerable, out, errs)
	}
	for _, want := range []string{
		"v/lib 1.0.0: 1 advisory", "CVE-2030-0001", "9.8 CRITICAL", "Heap overflow in the parser", "https://osv.dev/vulnerability/CVE-2030-0001",
		"v/solo 1.0.0: 1 advisory", "CVE-2030-0002", "LOW", "Details line one.",
		"2 advisories in 2 of 2 locked package(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "GHSA-aaaa") {
		t.Errorf("the same vulnerability must be reported once:\n%s", out)
	}
	if got := strings.Join(osvdb.queries, " "); !strings.Contains(got, "commit:"+solo) || !strings.Contains(got, "tag:https://example.com/v/lib@v1.0.0") || strings.Contains(got, "v/solo@") {
		t.Errorf("a pinned commit is looked up by commit, an unpinned one by tag: %s", got)
	}

	// accepted advisories do not fail the audit
	code, out, _ = runCLI(args("--ignore", "CVE-2030-0001", "--ignore", "cve-2030-0002")...)
	if code != exitOK || !strings.Contains(out, "No known vulnerabilities") || !strings.Contains(out, "2 advisories ignored") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// an alias also matches
	code, out, _ = runCLI(args("--ignore", "CVE-2030-0001")...)
	if code != exitVulnerable || strings.Contains(out, "v/lib 1.0.0:") {
		t.Errorf("exit %d\n%s", code, out)
	}

	var parsed struct {
		Advisories int
		Packages   []struct {
			Name            string
			Vulnerabilities []struct{ ID, Severity, URL string }
		}
	}
	_, js, _ := runCLI(args("--json")...)
	if err := json.Unmarshal([]byte(js), &parsed); err != nil || parsed.Advisories != 2 || len(parsed.Packages) != 2 || parsed.Packages[0].Vulnerabilities[0].Severity != "9.8 CRITICAL" {
		t.Errorf("%v %+v\n%s", err, parsed, js)
	}
}

func TestAuditNeedsALockAndReportsAnUnreachableDatabase(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/solo", "1.0.0")
	project := e.project(`"v/solo":"^1.0"`)
	code, _, errs := runCLI(explain(e, project, "audit", "--osv-url", "http://127.0.0.1:1")...)
	if code != exitError || !strings.Contains(errs, "kiln.lock") {
		t.Errorf("no lock: %d %s", code, errs)
	}
	mustRun(t, e.cmd("install", project)...)
	code, _, errs = runCLI(explain(e, project, "audit", "--osv-url", "http://127.0.0.1:1")...)
	if code != exitNetwork || !strings.Contains(errs, "cannot query") {
		t.Errorf("unreachable database: %d %s", code, errs)
	}
}

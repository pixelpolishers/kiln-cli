package main

import (
	"strings"
	"testing"
)

// A package with a prebuilt binary still needs what using it needs on this machine.
func TestSystemRequiresBlockEvenWithAPrebuiltBinary(t *testing.T) {
	e := newLockEnv(t)
	e.releaseWith("v/gui", "1.0.0", `,"system-requires":[
	  {"name":"The GUI SDK","env":"KILN_TEST_GUI_SDK","hint":{"linux":"install the SDK","windows":"install the SDK from the vendor"}},
	  {"name":"Only on another system","env":"KILN_TEST_NEVER_SET","when":{"os":"some-other-os"}},
	  {"name":"Nice to have","env":"KILN_TEST_NICE","optional":true}]`)
	project := e.project(`"v/gui":"^1.0"`)
	t.Setenv("KILN_TEST_GUI_SDK", "")

	code, _, errs := runCLI(e.cmd("install", project)...)
	if code != exitMissingReq {
		t.Fatalf("exit %d, want %d:\n%s", code, exitMissingReq, errs)
	}
	for _, want := range []string{"v/gui 1.0.0", "missing: The GUI SDK (needed to use the package)", "KILN_TEST_GUI_SDK is not set", "to fix:"} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in:\n%s", want, errs)
		}
	}
	for _, unwanted := range []string{"another system", "Nice to have"} {
		if strings.Contains(errs, unwanted) {
			t.Errorf("%q must not block:\n%s", unwanted, errs)
		}
	}

	// --check reports the same without installing anything
	if code, _, _ := runCLI(e.cmd("install", project, "--check")...); code != exitMissingReq {
		t.Errorf("--check: exit %d", code)
	}

	t.Setenv("KILN_TEST_GUI_SDK", "/opt/sdk")
	out := mustRun(t, e.cmd("install", project)...)
	if !strings.Contains(out, "1 package(s) ready") {
		t.Errorf("%s", out)
	}
	// the optional one only warns
	_, _, errs = runCLI(e.cmd("install", project)...)
	if !strings.Contains(errs, "Nice to have") {
		t.Errorf("an optional requirement is mentioned as a warning:\n%s", errs)
	}

	// once installed, a cached package is checked again on the next install
	t.Setenv("KILN_TEST_GUI_SDK", "")
	if code, _, _ := runCLI(e.cmd("install", project)...); code != exitMissingReq {
		t.Errorf("cached package must be re-checked: exit %d", code)
	}
}

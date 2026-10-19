package pack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pixelpolishers/kiln-core/manifest"
)

func TestDescribeKeepsOnlyTheIncludeDirectoriesThatApply(t *testing.T) {
	var m manifest.Package
	err := json.Unmarshal([]byte(`{"name":"v/gui","targets":{"core":{"type":"library","output-name":"core",
	  "include":["include/main",
	    {"value":"lib/setup-base","when":{"options.gui":false}},
	    {"value":"lib/setup-gtk","when":{"os":"linux","options.gui":true}}]}}}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	for _, d := range []string{"include/main", "lib/setup-base", "lib/setup-gtk"} {
		if err := os.MkdirAll(filepath.Join(prefix, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(prefix, "lib", "libcore.a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	smap := map[string]any{"os": "linux", "linkage": "static", "build_type": "release"}

	for gui, want := range map[bool][]string{
		true:  {"include/main", "lib/setup-gtk"},
		false: {"include/main", "lib/setup-base"},
	} {
		info, err := Describe(m, "1.0.0", prefix, smap, map[string]any{"gui": gui}, nil)
		if err != nil {
			t.Fatalf("gui=%v: %v", gui, err)
		}
		if got := info.Targets["core"].Include; !reflect.DeepEqual(got, want) {
			t.Errorf("gui=%v: include %v, want %v", gui, got, want)
		}
	}

	// a directory that applies but is not there is still an error
	if err := os.RemoveAll(filepath.Join(prefix, "lib", "setup-gtk")); err != nil {
		t.Fatal(err)
	}
	if _, err := Describe(m, "1.0.0", prefix, smap, map[string]any{"gui": true}, nil); err == nil {
		t.Error("a missing include directory must be reported")
	}
}

func TestATargetWithAConditionExistsOnlyWhereItMatches(t *testing.T) {
	var m manifest.Package
	err := json.Unmarshal([]byte(`{"name":"v/gui","targets":{
	  "base":{"type":"library","output-name":"base"},
	  "core":{"type":"library","output-name":"core","when":{"options.gui":true},"link-libraries":["base"]}}}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	// only libbase.a exists: with gui off, the missing libcore.a must not matter
	if err := os.WriteFile(filepath.Join(prefix, "lib", "libbase.a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	smap := map[string]any{"os": "linux", "linkage": "static", "build_type": "release"}

	info, err := Describe(m, "1.0.0", prefix, smap, map[string]any{"gui": false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := info.Targets["core"]; ok || len(info.Targets) != 1 {
		t.Errorf("gui=false: targets %v, want only base", info.Targets)
	}
	if _, err := Describe(m, "1.0.0", prefix, smap, map[string]any{"gui": true}, nil); err == nil {
		t.Error("gui=true: the missing libcore.a must be reported")
	}
}

func TestExecutablesAndLibrariesInSubdirectories(t *testing.T) {
	var m manifest.Package
	err := json.Unmarshal([]byte(`{"name":"v/tools","targets":{
	  "gen":{"type":"executable","output-name":"moc"},
	  "zlib":{"type":"library","output-name":"opencv4/3rdparty/zlib"}}}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	for _, f := range []string{"libexec/moc", "lib/opencv4/3rdparty/libzlib.a"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(prefix, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prefix, f), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(prefix, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	smap := map[string]any{"os": "linux", "linkage": "static", "build_type": "release"}
	info, err := Describe(m, "1.0.0", prefix, smap, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Targets["gen"].Exe; got != "libexec/moc" {
		t.Errorf("exe %q, want libexec/moc (Qt keeps its generators there)", got)
	}
	if got := info.Targets["zlib"].Libs; !reflect.DeepEqual(got, []string{"lib/opencv4/3rdparty/libzlib.a"}) {
		t.Errorf("libs %v", got)
	}
	if err := os.Remove(filepath.Join(prefix, "libexec", "moc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Describe(m, "1.0.0", prefix, smap, nil, nil); err == nil {
		t.Error("a missing program must be reported")
	}
}

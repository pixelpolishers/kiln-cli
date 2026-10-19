package project

import (
	"encoding/json"
	"strings"
	"testing"
)

const doc = `{
  "$schema": "../schema.json",
  "name": "me/project",
  "description": "keeps its place",
  "require": {
    "gabime/spdlog": "^1.14",
    "fmtlib/fmt": { "version": "~11.0.0", "options": { "header_only": true } }
  },
  "future-field": { "nested": [1, 2, 3] },
  "targets": { "app": { "type": "executable" } }
}`

func keys(t *testing.T, raw []byte) []string {
	t.Helper()
	o, err := parseObject(raw)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return o.keys
}

func TestSetRequireKeepsEverythingElse(t *testing.T) {
	out, err := SetRequire([]byte(doc), SectionRequire, "nlohmann/json", "^3.12.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys(t, out), ","); got != "$schema,name,description,require,future-field,targets" {
		t.Errorf("top-level key order changed: %s", got)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	req := m["require"].(map[string]any)
	if req["nlohmann/json"] != "^3.12.0" || req["gabime/spdlog"] != "^1.14" {
		t.Errorf("%v", req)
	}
	if m["future-field"].(map[string]any)["nested"].([]any)[2] != float64(3) || m["description"] != "keeps its place" {
		t.Errorf("unknown fields must survive: %v", m)
	}
	// dependencies are listed alphabetically, and the object form is untouched
	dep, _ := parseObject(mustField(t, out, "require"))
	if strings.Join(dep.keys, ",") != "fmtlib/fmt,gabime/spdlog,nlohmann/json" {
		t.Errorf("%v", dep.keys)
	}
	if !strings.Contains(string(dep.vals["fmtlib/fmt"]), "header_only") {
		t.Error("an object-form dependency must keep its options")
	}
	if !strings.HasSuffix(string(out), "}\n") || !strings.Contains(string(out), "\n  \"name\"") {
		t.Errorf("expected two-space indentation and a trailing newline:\n%s", out)
	}
}

func mustField(t *testing.T, raw []byte, key string) []byte {
	t.Helper()
	o, err := parseObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	return o.vals[key]
}

func TestSetRequireChangesOnlyTheVersionOfAnObjectDependency(t *testing.T) {
	out, err := SetRequire([]byte(doc), SectionRequire, "fmtlib/fmt", "^11.1")
	if err != nil {
		t.Fatal(err)
	}
	depRaw := mustField(t, mustField(t, out, "require"), "fmtlib/fmt")
	dep, err := parseObject(depRaw)
	if err != nil {
		t.Fatalf("the dependency must stay an object: %v (%s)", err, depRaw)
	}
	var v string
	json.Unmarshal(dep.vals["version"], &v)
	if v != "^11.1" || !strings.Contains(string(dep.vals["options"]), "header_only") {
		t.Errorf("%s", depRaw)
	}
}

func TestSetRequireCreatesSectionsAndMovesBetweenThem(t *testing.T) {
	out, err := SetRequire([]byte(`{"name":"me/p"}`), SectionRequire, "a/b", "^1.0.0")
	if err != nil || strings.Join(keys(t, out), ",") != "name,require" {
		t.Fatalf("%v %s", err, out)
	}
	out, err = SetRequire(out, SectionRequireDev, "a/b", "^1.0.0") // now a dev dependency: moved, not duplicated
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Require    map[string]any `json:"require"`
		RequireDev map[string]any `json:"require-dev"`
	}
	json.Unmarshal(out, &m)
	if len(m.Require) != 0 || m.RequireDev["a/b"] != "^1.0.0" {
		t.Errorf("%s", out)
	}
}

func TestRemoveRequire(t *testing.T) {
	out, found, err := RemoveRequire([]byte(doc), "gabime/spdlog")
	if err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	var m struct{ Require map[string]any }
	json.Unmarshal(out, &m)
	if _, still := m.Require["gabime/spdlog"]; still || len(m.Require) != 1 {
		t.Errorf("%v", m.Require)
	}
	out, found, _ = RemoveRequire(out, "fmtlib/fmt")
	if !found || !strings.Contains(string(out), `"require": {}`) {
		t.Errorf("removing the last dependency leaves an empty section:\n%s", out)
	}
	if _, found, _ = RemoveRequire(out, "nope/nope"); found {
		t.Error("an absent package must be reported")
	}
	// from require-dev too
	dev, _ := SetRequire([]byte(`{"name":"me/p"}`), SectionRequireDev, "t/t", "*")
	if _, found, _ = RemoveRequire(dev, "t/t"); !found {
		t.Error("must find it in require-dev")
	}
}

func TestEditRejectsBrokenDocuments(t *testing.T) {
	for _, bad := range []string{"", "[]", "{", `{"require": []}`} {
		if _, err := SetRequire([]byte(bad), SectionRequire, "a/b", "*"); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestInitial(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(Initial("me/new"), &m); err != nil || m["name"] != "me/new" {
		t.Errorf("%v %v", m, err)
	}
	// without a name there is no name key at all
	m = nil
	if err := json.Unmarshal(Initial(""), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["name"]; ok || m["require"] == nil {
		t.Errorf("a nameless manifest has only an empty require: %v", m)
	}
	// and it can be edited like any other
	out, err := SetRequire(Initial(""), SectionRequire, "a/b", "^1.0.0")
	if err != nil || !strings.Contains(string(out), `"a/b": "^1.0.0"`) {
		t.Errorf("%v\n%s", err, out)
	}
}

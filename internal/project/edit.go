package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// object is a JSON object that remembers the order of its keys, so editing a
// manifest does not shuffle it. Values stay raw: anything this package does not
// edit, including fields it does not know, is written back unchanged.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseObject(raw []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	o := &object{vals: map[string]json.RawMessage{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[key]; !dup {
			o.keys = append(o.keys, key)
		}
		o.vals[key] = v
	}
	end, err := dec.Token() // the closing brace: a truncated document ends here with an error
	if err != nil {
		return nil, err
	}
	if d, ok := end.(json.Delim); !ok || d != '}' {
		return nil, fmt.Errorf("malformed JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON object")
	}
	return o, nil
}

func (o *object) set(key string, val json.RawMessage) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

func (o *object) del(key string) bool {
	if _, ok := o.vals[key]; !ok {
		return false
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

func (o *object) sortKeys() { sort.Strings(o.keys) }

func (o *object) compact() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		if err := json.Compact(&b, o.vals[k]); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// pretty renders a document with two-space indentation and a final newline.
func pretty(compact []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Section names of the manifest that list dependencies.
const (
	SectionRequire    = "require"
	SectionRequireDev = "require-dev"
)

// SetRequire sets the constraint for name in the given section ("require" or
// "require-dev") of a manifest document, creating the section if needed and
// keeping the other section, key order and unknown fields as they were. If the
// dependency is written as an object ({"version": ..., "options": ...}) only
// its version changes. A package already present in the other section is moved.
func SetRequire(raw []byte, section, name, constraint string) ([]byte, error) {
	doc, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	other := SectionRequireDev
	if section == SectionRequireDev {
		other = SectionRequire
	}
	if err := removeFrom(doc, other, name); err != nil {
		return nil, err
	}

	deps := &object{vals: map[string]json.RawMessage{}}
	if existing, ok := doc.vals[section]; ok {
		if deps, err = parseObject(existing); err != nil {
			return nil, fmt.Errorf("%s: %q: %w", ManifestFile, section, err)
		}
	}
	value, _ := json.Marshal(constraint)
	if old, ok := deps.vals[name]; ok {
		if dep, err := parseObject(old); err == nil { // the object form: change only the version
			dep.set("version", value)
			if value, err = dep.compact(); err != nil {
				return nil, err
			}
		}
	}
	deps.set(name, value)
	deps.sortKeys()
	return write(doc, section, deps)
}

// RemoveRequire removes name from require and require-dev. It returns false if
// the package was in neither.
func RemoveRequire(raw []byte, name string) ([]byte, bool, error) {
	doc, err := parseObject(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	found := false
	for _, section := range []string{SectionRequire, SectionRequireDev} {
		had, err := has(doc, section, name)
		if err != nil {
			return nil, false, err
		}
		if had {
			found = true
			if err := removeFrom(doc, section, name); err != nil {
				return nil, false, err
			}
		}
	}
	out, err := doc.compact()
	if err != nil {
		return nil, false, err
	}
	res, err := pretty(out)
	return res, found, err
}

// has reports whether the section of doc lists name.
func has(doc *object, section, name string) (bool, error) {
	existing, ok := doc.vals[section]
	if !ok {
		return false, nil
	}
	deps, err := parseObject(existing)
	if err != nil {
		return false, fmt.Errorf("%s: %q: %w", ManifestFile, section, err)
	}
	_, ok = deps.vals[name]
	return ok, nil
}

func removeFrom(doc *object, section, name string) error {
	existing, ok := doc.vals[section]
	if !ok {
		return nil
	}
	deps, err := parseObject(existing)
	if err != nil {
		return fmt.Errorf("%s: %q: %w", ManifestFile, section, err)
	}
	if !deps.del(name) {
		return nil
	}
	c, err := deps.compact()
	if err != nil {
		return err
	}
	doc.set(section, c)
	return nil
}

func write(doc *object, section string, deps *object) ([]byte, error) {
	c, err := deps.compact()
	if err != nil {
		return nil, err
	}
	doc.set(section, c)
	out, err := doc.compact()
	if err != nil {
		return nil, err
	}
	return pretty(out)
}

// Initial returns the text of a new minimal manifest. A project that only uses
// libraries needs no name, so an empty name leaves it out.
func Initial(name string) []byte {
	if name == "" {
		return []byte("{\n  \"require\": {}\n}\n")
	}
	b, _ := json.MarshalIndent(map[string]any{"name": name, "require": map[string]string{}}, "", "  ")
	return append(b, '\n')
}

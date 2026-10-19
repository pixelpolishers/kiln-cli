// Package settings detects the build settings of the current machine and
// applies user overrides. The resulting object follows settings.schema.json.
package settings

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type Compiler struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Settings struct {
	OS          string   `json:"os"`
	Arch        string   `json:"arch"`
	Compiler    Compiler `json:"compiler"`
	Stdlib      string   `json:"stdlib"`
	Libc        string   `json:"libc,omitempty"`
	CppStandard string   `json:"cpp_standard"`
	BuildType   string   `json:"build_type"`
	Linkage     string   `json:"linkage"`
	MSVCRuntime string   `json:"msvc_runtime,omitempty"`
	CXX11ABI    *bool    `json:"cxx11_abi,omitempty"`
	Exceptions  *bool    `json:"exceptions,omitempty"`
	RTTI        *bool    `json:"rtti,omitempty"`
	LTO         *bool    `json:"lto,omitempty"`
}

// Sources records where each setting came from: "detected", "default" or "override".
type Sources map[string]string

const (
	Detected = "detected"
	Default  = "default"
	Override = "override"
)

// Keys accepted by -s key=value, in display order.
var Keys = []string{
	"os", "arch", "compiler.name", "compiler.version", "stdlib", "libc",
	"cpp_standard", "build_type", "linkage", "msvc_runtime",
	"cxx11_abi", "exceptions", "rtti", "lto",
}

// Apply sets overrides given as "key=value". Values are validated later
// against the schema; Apply only rejects unknown keys and malformed booleans.
func (s *Settings) Apply(overrides []string, src Sources) error {
	for _, o := range overrides {
		k, v, ok := strings.Cut(o, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return fmt.Errorf("invalid setting %q: want key=value", o)
		}
		boolPtr := func() (*bool, error) {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("setting %s: %q is not a boolean", k, v)
			}
			return &b, nil
		}
		var err error
		switch k {
		case "os":
			s.OS = v
		case "arch":
			s.Arch = v
		case "compiler.name":
			s.Compiler.Name = v
		case "compiler.version":
			s.Compiler.Version = v
		case "stdlib":
			s.Stdlib = v
		case "libc":
			s.Libc = v
		case "cpp_standard":
			s.CppStandard = v
		case "build_type":
			s.BuildType = v
		case "linkage":
			s.Linkage = v
		case "msvc_runtime":
			s.MSVCRuntime = v
		case "cxx11_abi":
			s.CXX11ABI, err = boolPtr()
		case "exceptions":
			s.Exceptions, err = boolPtr()
		case "rtti":
			s.RTTI, err = boolPtr()
		case "lto":
			s.LTO, err = boolPtr()
		default:
			return fmt.Errorf("unknown setting %q (known: %s)", k, strings.Join(Keys, ", "))
		}
		if err != nil {
			return err
		}
		src[k] = Override
	}
	return nil
}

// Value returns the display value of a key from Keys ("" if unset).
func (s *Settings) Value(key string) string {
	b := func(p *bool) string {
		if p == nil {
			return ""
		}
		return strconv.FormatBool(*p)
	}
	switch key {
	case "os":
		return s.OS
	case "arch":
		return s.Arch
	case "compiler.name":
		return s.Compiler.Name
	case "compiler.version":
		return s.Compiler.Version
	case "stdlib":
		return s.Stdlib
	case "libc":
		return s.Libc
	case "cpp_standard":
		return s.CppStandard
	case "build_type":
		return s.BuildType
	case "linkage":
		return s.Linkage
	case "msvc_runtime":
		return s.MSVCRuntime
	case "cxx11_abi":
		return b(s.CXX11ABI)
	case "exceptions":
		return b(s.Exceptions)
	case "rtti":
		return b(s.RTTI)
	case "lto":
		return b(s.LTO)
	}
	return ""
}

// Map returns the settings as the generic JSON object that asset ids and
// conditions work with.
func (s Settings) Map() (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}

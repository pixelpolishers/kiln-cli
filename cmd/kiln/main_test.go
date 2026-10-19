package main

import (
	"os"
	"testing"
)

// Most tests install packages whose repositories do not exist; they must not
// try to fetch sources for vendor/kiln. Tests of that feature clear the variable.
func TestMain(m *testing.M) {
	os.Setenv("KILN_NO_SOURCES", "1")
	os.Exit(m.Run())
}

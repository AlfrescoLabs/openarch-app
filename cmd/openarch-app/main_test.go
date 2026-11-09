package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"openarch-app": func() { os.Exit(run()) },
		"openarch":     fakeOpenArch,
	})
}

// fakeOpenArch stands in for the CLI for OpenArch. It prints the version JSON
// with the version in FAKE_OPENARCH_VERSION (default 0.5.2).
func fakeOpenArch() {
	v := os.Getenv("FAKE_OPENARCH_VERSION")
	if v == "" {
		v = "0.5.2"
	}
	fmt.Printf("{\"version\": %q}\n", v)
	os.Exit(0)
}

func TestScript(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("..", "..", "testdata", "script"),
	})
}

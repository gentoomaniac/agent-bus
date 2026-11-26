package gocli

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/alecthomas/kong"
)

func TestVersionFlagPrintsTheBuildInfoAndExits(t *testing.T) {
	var app struct {
		Version VersionFlag `short:"V"`
	}
	var stdout bytes.Buffer
	exitCode := -1
	parser, err := kong.New(&app,
		kong.Writers(&stdout, &stdout),
		kong.Exit(func(code int) { exitCode = code }),
		kong.Vars{"version": "1.2.3", "commit": "abc1234", "binName": "tool", "builtBy": "ci/7", "date": "2026-11-26"})
	if err != nil {
		t.Fatal(err)
	}
	// kong.Exit does not stop the parse, so its error is not the point here.
	_, _ = parser.Parse([]string{"-V"})

	want := "tool commit:abc1234 release:1.2.3 build:ci/7 date:2026-11-26 goVersion:" + runtime.Version() +
		" platform:" + runtime.GOOS + "/" + runtime.GOARCH + "\n"
	if stdout.String() != want {
		t.Errorf("output %q, want %q", stdout.String(), want)
	}
	if exitCode != 0 {
		t.Errorf("exit code %d, want 0", exitCode)
	}
}

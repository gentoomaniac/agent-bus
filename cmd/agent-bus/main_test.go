package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enrollclient"
)

// The event hook must stay silent and succeed whatever it is fed and wherever it runs.
func TestEventCommandIsSilentAndSucceedsOnBadInput(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, input := range []string{"", "not json", `{"tool_name":`, `{"tool_name":"Edit","tool_input":"string"}`} {
		stdin, stdinWriter, _ := os.Pipe()
		stdout, stdoutWriter, _ := os.Pipe()
		realStdin, realStdout := os.Stdin, os.Stdout
		os.Stdin, os.Stdout = stdin, stdoutWriter
		stdinWriter.WriteString(input)
		stdinWriter.Close()

		err := (&EventCmd{Kind: "pre-tool"}).Run()

		os.Stdin, os.Stdout = realStdin, realStdout
		stdoutWriter.Close()
		printed, _ := io.ReadAll(stdout)
		if err != nil || len(printed) != 0 {
			t.Errorf("input %q: error %v, printed %q", input, err, printed)
		}
	}
}

func TestWaitForCertTakesAnOptionalDuration(t *testing.T) {
	for args, want := range map[string]time.Duration{
		"":                          0,
		"--waitforcert":             enrollclient.DefaultWait,
		"--waitforcert=5m":          5 * time.Minute,
		"--waitforcert --agent a@b": enrollclient.DefaultWait,
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.Parse(append([]string{"enroll"}, strings.Fields(args)...)); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
		if got := cli.Enroll.WaitForCert.max; got != want {
			t.Errorf("%q: wait %v, want %v", args, got, want)
		}
	}
	for _, bad := range []string{"--waitforcert=soon", "--waitforcert=-1m"} {
		var cli CLI
		parser, _ := kong.New(&cli)
		if _, err := parser.Parse([]string{"enroll", bad}); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

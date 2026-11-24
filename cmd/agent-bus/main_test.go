package main

import (
	"io"
	"os"
	"testing"
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

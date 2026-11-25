package main

import (
	"io"
	"os"
	"path/filepath"
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

func TestBrokerIsTheFlagThenTheEnrolledOneThenTheDefault(t *testing.T) {
	configDir := t.TempDir()
	const agent = "lib-x@bob"
	dir := filepath.Join(configDir, agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	cli := CLI{ConfigDir: configDir, Agent: agent}
	if _, note, err := cli.chooseBroker(dir); err != nil || cli.Broker != defaultBroker || note != "" {
		t.Errorf("no broker, no certificate: broker %q, note %q, %v, want the default without a warning", cli.Broker, note, err)
	}

	if err := os.WriteFile(enrollclient.BrokerPath(configDir, agent), []byte("bus.example.org:4222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli = CLI{ConfigDir: configDir, Agent: agent}
	if source, note, err := cli.chooseBroker(dir); err != nil || cli.Broker != "bus.example.org:4222" || note != "" || !strings.Contains(source, "enrolled") {
		t.Errorf("recorded: broker %q (%s), note %q, %v", cli.Broker, source, note, err)
	}

	cli = CLI{ConfigDir: configDir, Agent: "other@bob"}
	if _, _, err := cli.chooseBroker(filepath.Join(configDir, "other@bob")); err != nil || cli.Broker != defaultBroker {
		t.Errorf("another identity must not take lib-x's broker: %q, %v", cli.Broker, err)
	}

	cli = CLI{ConfigDir: configDir, Agent: agent, Broker: "other:4222"}
	if _, _, err := cli.chooseBroker(dir); err != nil || cli.Broker != "other:4222" {
		t.Errorf("--broker: broker %q, %v, want it kept", cli.Broker, err)
	}
}

func TestAnEnrolledIdentityWithoutARecordedBrokerWarnsThatItDialsLocalhost(t *testing.T) {
	configDir := t.TempDir()
	const agent = "lib-x@bob"
	dir := filepath.Join(configDir, agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"client.pem", "client.key"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cli := CLI{ConfigDir: configDir, Agent: agent}

	_, note, err := cli.chooseBroker(dir)

	for _, want := range []string{agent, defaultBroker, "--broker", "BUS_BROKER", "enroll"} {
		if err != nil || !strings.Contains(note, want) {
			t.Errorf("note %q (%v) lacks %q", note, err, want)
		}
	}
	if strings.Contains(note, "never enrolled") {
		t.Errorf("note %q claims the identity never enrolled", note)
	}
}

func TestAnIdentityThatWasNeverEnrolledNamesTheOnesThatWere(t *testing.T) {
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, "lib-x@bob"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "lib-x@bob", "key"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"directory", "no-key@bob"} { // not identities: no valid ID, no key
		if err := os.MkdirAll(filepath.Join(configDir, other), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cli := CLI{Agent: "api-client@alice", ConfigDir: configDir}

	_, _, err := cli.open(false)

	if err == nil || !strings.Contains(err.Error(), "api-client@alice") || !strings.Contains(err.Error(), "lib-x@bob") ||
		strings.Contains(err.Error(), "directory") || strings.Contains(err.Error(), "no-key") {
		t.Errorf("open = %v, want an error naming the unknown identity and only lib-x@bob as known", err)
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

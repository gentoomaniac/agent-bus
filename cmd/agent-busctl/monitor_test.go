package main

import (
	"io"
	"strings"
	"testing"
)

func TestReadNewPasswordFromStandardInput(t *testing.T) {
	for input, want := range map[string]string{"s3cret pass\n": "s3cret pass", "no newline": "no newline", "crlf\r\nrest\n": "crlf"} {
		got, err := readNewPassword(strings.NewReader(input), io.Discard)
		if err != nil || got != want {
			t.Errorf("input %q: %q, %v, want %q", input, got, err, want)
		}
	}
	if _, err := readNewPassword(strings.NewReader(""), io.Discard); err == nil {
		t.Error("empty input was accepted")
	}
}

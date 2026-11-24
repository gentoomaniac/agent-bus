package adapter

import (
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

func TestRateLimiterAllowsTheLimitPerWindow(t *testing.T) {
	limiter := rateLimiter{limit: 3, window: 10 * time.Minute}
	start := time.Now()
	for i := 0; i < 3; i++ {
		if ok, _ := limiter.allow(start.Add(time.Duration(i) * time.Minute)); !ok {
			t.Fatalf("send %d was refused", i+1)
		}
	}
	if ok, retryAfter := limiter.allow(start.Add(5 * time.Minute)); ok || retryAfter != 5*time.Minute {
		t.Errorf("a fourth send inside the window: allowed %v, retry after %s, want 5m0s", ok, retryAfter)
	}
	if ok, _ := limiter.allow(start.Add(10*time.Minute + time.Second)); !ok {
		t.Error("the allowance did not come back when the first send left the window")
	}
}

func TestFindSecret(t *testing.T) {
	secrets := []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nabc",
		"key AKIAIOSFODNN7EXAMPLE here",
		"ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789",
		"export API_KEY=abcdef1234567890",
		`{"password": "hunter2hunter2hunter2"}`,
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789",
	}
	for _, text := range secrets {
		if findSecret(envelope.Body{Text: text}) == "" {
			t.Errorf("not found: %q", text)
		}
	}
	if findSecret(envelope.Body{Title: "token", Refs: []string{"-----BEGIN PRIVATE KEY-----"}}) == "" {
		t.Error("a secret in refs was not found")
	}
	for _, text := range []string{"Expose retry config on Client", "the password is checked in auth.go", "see https://github.com/org/lib-x/blob/main/a.go", "token = x"} {
		if found := findSecret(envelope.Body{Text: text}); found != "" {
			t.Errorf("%q flagged as %s", text, found)
		}
	}
}

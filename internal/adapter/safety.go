package adapter

import (
	"regexp"
	"strings"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// rateLimiter allows a number of sends per window (design.md §8 rule 4). It lives in memory: a
// restart gives a fresh allowance, which is enough to stop a runaway loop.
type rateLimiter struct {
	limit  int
	window time.Duration
	sent   []time.Time
}

// allow records a send at now if the allowance is not used up.
func (limiter *rateLimiter) allow(now time.Time) bool {
	cutoff := now.Add(-limiter.window)
	for len(limiter.sent) > 0 && limiter.sent[0].Before(cutoff) {
		limiter.sent = limiter.sent[1:]
	}
	if len(limiter.sent) >= limiter.limit {
		return false
	}
	limiter.sent = append(limiter.sent, now)
	return true
}

// secretPatterns are the obvious secrets of §8 rule 5: private keys and well-known token formats.
var secretPatterns = map[string]*regexp.Regexp{
	"a private key":         regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	"an AWS access key":     regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	"a GitHub token":        regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})\b`),
	"a Slack token":         regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	"an Anthropic API key":  regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`),
	"a bearer token":        regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{30,}`),
	"a password or API key": regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\b["']?\s*[:=]\s*["']?[^\s"']{12,}`),
}

// findSecret says what kind of obvious secret the body contains, or "" if none.
func findSecret(body envelope.Body) string {
	text := strings.Join(append([]string{body.Title, body.Text}, body.Refs...), "\n")
	for name, pattern := range secretPatterns {
		if pattern.MatchString(text) {
			return name
		}
	}
	return ""
}

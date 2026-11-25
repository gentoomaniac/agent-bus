package broker

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// A max age NATS would refuse is refused before any stream is touched, with a message naming it.
func TestSetupRefusesAMaxAgeBelowTheDedupeWindow(t *testing.T) {
	for _, maxAge := range []time.Duration{time.Hour, -time.Hour} {
		err := (&Admin{}).Setup(context.Background(), bus.Retention{MaxAge: maxAge, MaxBytes: -1})
		if err == nil || !strings.Contains(err.Error(), "dedupe window") {
			t.Errorf("max age %s: got %v, want a refusal naming the dedupe window", maxAge, err)
		}
	}
}

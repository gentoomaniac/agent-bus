package main

import (
	"context"
	"fmt"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

type InitCmd struct {
	MaxAge   time.Duration `help:"Delete messages older than this from the log; 0 keeps them forever, else at least the 24h dedupe window." default:"2160h" env:"BUS_MAX_AGE"`
	MaxBytes int64         `help:"Delete the oldest messages once the log is larger than this many bytes; -1 sets no limit." default:"1073741824" env:"BUS_MAX_BYTES"`
}

// Run creates the streams and buckets of docs/design/jetstream-binding.md. Existing ones keep their
// data; the limits apply to an existing BUS stream too, so a log that is over them loses its
// oldest messages at once.
func (cmd *InitCmd) Run(ctx context.Context, cli *CLI) error {
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	if err := admin.Setup(ctx, bus.Retention{MaxAge: cmd.MaxAge, MaxBytes: cmd.MaxBytes}); err != nil {
		return err
	}
	fmt.Printf("streams BUS (keeps %s, %s), PRESENCE and LEDGER and buckets registry and catalogue are in place, agent grants are up to date\n", keepsFor(cmd.MaxAge), sizeLimit(cmd.MaxBytes))
	return nil
}

func keepsFor(maxAge time.Duration) string {
	if maxAge == 0 {
		return "forever"
	}
	return maxAge.String()
}

func sizeLimit(maxBytes int64) string {
	if maxBytes < 0 {
		return "no size limit"
	}
	return fmt.Sprintf("at most %d bytes", maxBytes)
}

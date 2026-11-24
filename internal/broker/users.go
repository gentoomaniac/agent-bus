package broker

import (
	"context"
	"fmt"

	"github.com/twmb/franz-go/pkg/kadm"
)

// HasSCRAMUser reports whether the broker has SCRAM credentials for the user.
func (admin *Admin) HasSCRAMUser(ctx context.Context, user string) (bool, error) {
	described, err := admin.DescribeUserSCRAMs(ctx, user)
	if err != nil {
		return false, err
	}
	return len(described[user].CredInfos) > 0, nil
}

// scramIterations is within the 4096..16384 the Kafka protocol allows.
const scramIterations = 8192

// SetSCRAMUser creates the user or replaces its password.
func (admin *Admin) SetSCRAMUser(ctx context.Context, user, password string) error {
	altered, err := admin.AlterUserSCRAMs(ctx, nil, []kadm.UpsertSCRAM{{
		User: user, Mechanism: kadm.ScramSha256, Iterations: scramIterations, Password: password,
	}})
	if err != nil {
		return err
	}
	if err := altered.Error(); err != nil {
		return fmt.Errorf("setting SCRAM credentials of %s: %w", user, err)
	}
	return nil
}

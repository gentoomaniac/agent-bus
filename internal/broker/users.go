package broker

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

// HasSCRAMUser reports whether the broker has SCRAM credentials for the user.
func (admin *Admin) HasSCRAMUser(ctx context.Context, user string) (bool, error) {
	described, err := admin.DescribeUserSCRAMs(ctx, user)
	if err != nil {
		return false, err
	}
	return hasCredentials(described, user)
}

// hasCredentials reads the broker's answer. RESOURCE_NOT_FOUND, which the broker reports for a user
// that does not exist or has no credentials, is the only per-user error that means "absent".
func hasCredentials(described kadm.DescribedUserSCRAMs, user string) (bool, error) {
	result, ok := described[user]
	if !ok {
		return false, fmt.Errorf("the broker's answer has no entry for user %s", user)
	}
	if errors.Is(result.Err, kerr.ResourceNotFound) {
		return false, nil
	}
	if result.Err != nil {
		return false, fmt.Errorf("describing SCRAM credentials of %s: %w", user, result.Err)
	}
	return len(result.CredInfos) > 0, nil
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

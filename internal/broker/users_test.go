package broker

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

func TestHasCredentials(t *testing.T) {
	tests := []struct {
		name      string
		described kadm.DescribedUserSCRAMs
		want      bool
		wantErr   bool
	}{
		{"user with credentials", kadm.DescribedUserSCRAMs{"u": {CredInfos: []kadm.CredInfo{{}}}}, true, false},
		{"explicit not found", kadm.DescribedUserSCRAMs{"u": {Err: kerr.ResourceNotFound}}, false, false},
		{"entry without credentials", kadm.DescribedUserSCRAMs{"u": {}}, false, false},
		{"other per-user error", kadm.DescribedUserSCRAMs{"u": {Err: errors.New("broker unhappy")}}, false, true},
		{"user missing from the result", kadm.DescribedUserSCRAMs{}, false, true},
	}
	for _, test := range tests {
		got, err := hasCredentials(test.described, "u")
		if got != test.want || (err != nil) != test.wantErr {
			t.Errorf("%s: got (%v, %v), want (%v, error %v)", test.name, got, err, test.want, test.wantErr)
		}
	}
}

package busd

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestDirectoryHoldsTheRegistryRecordsAndAnUnreadableRegistryIsAnError(t *testing.T) {
	f := newFixture(t)
	f.register("alice@x", f.newKey())
	f.register("bob@x", f.newKey())

	reply := f.service.Directory(context.Background())
	if reply.Error != "" || len(reply.Records) != 2 {
		t.Fatalf("directory: %d records, error %q, want 2 and none", len(reply.Records), reply.Error)
	}

	f.registry.down = errors.New("kv unreachable")
	if reply := f.service.Directory(context.Background()); reply.Error == "" || len(reply.Records) != 0 {
		t.Errorf("an unreadable registry answered %+v, want an error and no records", reply)
	}
}

func TestDirectoryRequestsAreRateLimited(t *testing.T) {
	f := newFixture(t)
	f.service.directoryLimiter = rate.NewLimiter(rate.Every(time.Hour), 1)
	if reply := f.service.Directory(context.Background()); reply.Error != "" {
		t.Fatalf("the first request: %s", reply.Error)
	}
	if reply := f.service.Directory(context.Background()); reply.Error == "" {
		t.Error("the second request was answered although the limit is one an hour")
	}
}

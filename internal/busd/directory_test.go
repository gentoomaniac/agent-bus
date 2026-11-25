package busd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestDirectoryIsSignedByTheCAAndHoldsTheRegistryRecords(t *testing.T) {
	f := newFixture(t)
	f.register("alice@x", f.newKey())
	f.register("bob@x", f.newKey())

	reply := f.service.Directory(context.Background())
	if reply.Error != "" {
		t.Fatal(reply.Error)
	}
	directory, issuedAt, err := reply.Verify(mustRead(t, filepath.Join(f.dir, "ca.pem")))
	if err != nil || len(directory.Records) != 2 {
		t.Fatalf("directory: %d records, %v, want 2 and none", len(directory.Records), err)
	}
	if !issuedAt.Equal(f.now) {
		t.Errorf("issued at %v, want %v", issuedAt, f.now)
	}
}

func TestAnUnreadableRegistryGivesAnErrorAndNoDirectory(t *testing.T) {
	f := newFixture(t)
	f.registry.down = errors.New("kv unreachable")

	reply := f.service.Directory(context.Background())

	if reply.Error == "" || len(reply.Payload) != 0 {
		t.Errorf("an unreadable registry answered %+v, want an error and no directory", reply)
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

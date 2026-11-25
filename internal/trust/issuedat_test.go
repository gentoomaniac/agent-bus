package trust

import (
	"context"
	"testing"
	"time"
)

func TestIssuedAtRoundTripsAndIsNotTakenForAnAgentRecord(t *testing.T) {
	dir := FileDirectory(t.TempDir())
	if issuedAt, err := dir.IssuedAt(); err != nil || !issuedAt.IsZero() {
		t.Fatalf("issued_at of an empty directory = %v, %v, want the zero time", issuedAt, err)
	}
	want := time.Date(2026, 11, 25, 12, 0, 0, 123, time.UTC)
	if err := dir.WriteIssuedAt(want); err != nil {
		t.Fatal(err)
	}
	if got, err := dir.IssuedAt(); err != nil || !got.Equal(want) {
		t.Errorf("issued_at = %v, %v, want %v", got, err, want)
	}
	if records, err := dir.Records(context.Background()); err != nil || len(records) != 0 {
		t.Errorf("records = %v, %v: the issued_at file must not count as a record", records, err)
	}
}

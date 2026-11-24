package adapter

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHandledSetSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handled.jsonl")
	set, err := openHandledSet(path)
	if err != nil {
		t.Fatal(err)
	}
	message := handledEntry{Kind: entryMessage, Destination: "general", Sequence: 7, From: "a@o", ID: "ID1", Digest: "d1", Thread: "T", Hops: 2, At: time.Now()}
	rejected := handledEntry{Kind: entryRejected, Destination: "general", Sequence: 8, At: time.Now()}
	for _, entry := range []handledEntry{message, rejected} {
		if err := set.add(entry); err != nil {
			t.Fatal(err)
		}
	}
	set.close()

	set, err = openHandledSet(path)
	if err != nil {
		t.Fatal(err)
	}
	defer set.close()
	if !set.sequenceHandled("general", 7) || !set.sequenceHandled("general", 8) {
		t.Error("handled sequences were lost")
	}
	if set.sequenceHandled("general", 9) || set.sequenceHandled("DM a@o", 7) {
		t.Error("an unhandled sequence or another destination counts as handled")
	}
	if got, ok := set.message("general", "a@o", "ID1"); !ok || got.Hops != 2 || got.Digest != "d1" {
		t.Errorf("message = %+v, %v", got, ok)
	}
	if _, ok := set.message("general", "b@o", "ID1"); ok {
		t.Error("the same ID from another sender counts as handled")
	}
	if got, ok := set.messageByID("ID1"); !ok || got.Thread != "T" {
		t.Errorf("messageByID = %+v, %v", got, ok)
	}
}

func TestRejectedDeliveryIsKnownBySequenceNotByID(t *testing.T) {
	set, err := openHandledSet(filepath.Join(t.TempDir(), "handled.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer set.close()
	if err := set.add(handledEntry{Kind: entryRejected, Destination: "general", Sequence: 3, ID: "COPIED"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := set.message("general", "", "COPIED"); ok {
		t.Error("a rejected delivery made its envelope ID count as handled")
	}
	if _, ok := set.messageByID("COPIED"); ok {
		t.Error("a rejected delivery's ID is a reply_to parent")
	}
}

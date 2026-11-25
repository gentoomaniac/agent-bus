package trust

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

func TestCatalogueRoundTripsAndIsNotTakenForAnAgentRecord(t *testing.T) {
	dir := FileDirectory(t.TempDir())
	if catalogue, err := dir.Catalogue(); err != nil || len(catalogue) != 0 {
		t.Fatalf("catalogue of an empty directory = %v, %v", catalogue, err)
	}
	if err := dir.Write(bus.AgentRecord{AgentID: lib}); err != nil {
		t.Fatal(err)
	}
	want := Catalogue{"dev": {lib}, "empty": {}}
	if err := dir.WriteCatalogue(want); err != nil {
		t.Fatal(err)
	}
	if got, err := dir.Catalogue(); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("catalogue = %v, %v, want %v", got, err, want)
	}
	if records, err := dir.Records(context.Background()); err != nil || len(records) != 1 {
		t.Errorf("records = %v, %v: the catalogue file must not count as a record", records, err)
	}
}

func TestWriteAndLookupRefuseAnIDThatLeavesTheDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := FileDirectory(filepath.Join(parent, "directory"))
	if err := os.Mkdir(string(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, agentID := range []string{"../x@y", "x@../y", "a/b@c", ""} {
		if err := dir.Write(bus.AgentRecord{AgentID: agentID}); err == nil {
			t.Errorf("Write accepted the ID %q", agentID)
		}
		if _, err := dir.Lookup(context.Background(), agentID); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lookup of %q = %v, want a refusal of the ID", agentID, err)
		}
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 1 {
		t.Errorf("the parent directory holds %d entries, want only the directory", len(entries))
	}
	if entries, _ := os.ReadDir(string(dir)); len(entries) != 0 {
		t.Errorf("the directory holds %d entries, want none", len(entries))
	}
}

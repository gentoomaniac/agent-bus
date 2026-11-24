package trust

import (
	"context"
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

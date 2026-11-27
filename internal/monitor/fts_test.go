package monitor

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// ftsMonitor holds a few messages whose words and filters the tests below search for.
func ftsMonitor(t *testing.T) *Monitor {
	t.Helper()
	monitor := New(Config{Report: failOnError(t)})
	day := func(d, hour int) time.Time { return time.Date(2026, 11, d, hour, 0, 0, 0, time.UTC) }
	for _, message := range []Message{
		{Sequence: 1, Channel: "group:ops", From: "Watcher-Sandbox@gentoomaniac", To: "ops", Type: "question", Targets: []string{"bob@home"},
			Title: "Deploy lib-x?", Text: "Can anyone deploy Überlib today? @bob@home", Time: day(20, 15)},
		{Sequence: 2, Channel: "dm:alice@home:bob@home", From: "alice@home", To: "bob@home", Type: "message", Text: "back soon, deployment later", Time: day(21, 9)},
		{Sequence: 3, Channel: "general", From: "bob@home", To: "general", Type: "answer", Title: "Cake", Text: "---", Time: day(22, 9)},
		{Sequence: 4, Channel: "general", Status: Rejected, Reason: "not json", Text: "deploy hidden"}, // not an envelope: From is empty
	} {
		message.Status = cmp.Or(message.Status, Verified)
		monitor.add(message)
	}
	return monitor
}

func sequencesFound(t *testing.T, monitor *Monitor, text string) []uint64 {
	t.Helper()
	result, err := monitor.Search(context.Background(), mustParse(t, text), 50, "")
	if err != nil {
		t.Fatalf("search %q: %v", text, err)
	}
	found := []uint64{}
	for _, hit := range result.Hits {
		found = append(found, hit.Sequence)
	}
	return found
}

func TestSearchMapsTheQueryLanguageToTheIndex(t *testing.T) {
	monitor := ftsMonitor(t)
	tests := []struct {
		query string
		want  []uint64
	}{
		{"DEPLOY", []uint64{2, 1}}, // case-insensitive; "deploy" starts "deployment" too
		{"deploy anyone", []uint64{1}},
		{"deploy nobody", []uint64{}},
		{"lib-x", []uint64{1}},
		{"lib", []uint64{1}},
		{`"anyone deploy"`, []uint64{1}},
		{`"deploy anyone"`, []uint64{}},
		{`"deploy lib-x"`, []uint64{1}},
		{"überlib", []uint64{1}},
		{"uberlib", []uint64{1}}, // the index folds diacritics
		{"@bob@home", []uint64{1}},
		{"ack", []uint64{}}, // a word start, not inside "back"
		{"back", []uint64{2}},
		{"cake", []uint64{3}}, // the title alone
		{"---", []uint64{}},   // no letter or digit: nothing to find
		{"AND", []uint64{}},   // FTS keywords are plain words
		{`NOT deploy`, []uint64{}},
		{`"a"b`, []uint64{}},
		{"hidden", []uint64{}}, // the record that is no envelope
	}
	for _, test := range tests {
		if got := sequencesFound(t, monitor, test.query); !slices.Equal(got, test.want) {
			t.Errorf("search %q found %v, want %v", test.query, got, test.want)
		}
	}
}

func TestSearchFiltersWordsAndFiltersAlike(t *testing.T) {
	monitor := ftsMonitor(t)
	tests := []struct {
		query string
		want  []uint64
	}{
		{"deploy from:watcher-sandbox", []uint64{1}},
		{"deploy from:alice", []uint64{2}},
		{"deploy from:nobody", []uint64{}},
		{"deploy to:#ops", []uint64{1}},
		{"deploy to:bob", []uint64{2}},
		{"deploy type:question", []uint64{1}},
		{"deploy type:question type:message", []uint64{2, 1}},
		{"deploy has:target", []uint64{1}},
		{"deploy targets:bob", []uint64{1}},
		{"deploy after:2026-11-21", []uint64{2}},
		{"deploy before:2026-11-21", []uint64{1}},
		{"deploy after:2026-11-20t16:00:00z", []uint64{2}},
		{"from:bob", []uint64{3}}, // filters alone never touch the index
		{"after:2026-11-21", []uint64{3, 2}},
		{"before:2026-12-01 type:question", []uint64{1}},
	}
	for _, test := range tests {
		if got := sequencesFound(t, monitor, test.query); !slices.Equal(got, test.want) {
			t.Errorf("search %q found %v, want %v", test.query, got, test.want)
		}
	}
}

func TestSearchSnippetsComeFromTheBody(t *testing.T) {
	monitor := ftsMonitor(t)
	result, err := monitor.Search(context.Background(), mustParse(t, "anyone"), 10, "")
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("search = %+v, %v", result, err)
	}
	hit := result.Hits[0]
	if hit.Title != "Deploy lib-x?" || hit.Snippet.Field != "text" || len(hit.Snippet.Matches) != 1 {
		t.Fatalf("hit = %+v", hit)
	}
	if match := hit.Snippet.Matches[0]; string([]rune(hit.Snippet.Text)[match[0]:match[1]]) != "anyone" {
		t.Errorf("the offsets mark %+v of %q", match, hit.Snippet.Text)
	}
	if folded, _ := monitor.Search(context.Background(), mustParse(t, "uberlib"), 10, ""); len(folded.Hits) != 1 || folded.Hits[0].Snippet.Text == "" {
		t.Errorf("a hit the snippet cannot mark still shows the start of the text: %+v", folded)
	}
}

func TestADeletedMessageIsNotFound(t *testing.T) {
	monitor := ftsMonitor(t)
	if got := sequencesFound(t, monitor, "deploy"); !slices.Equal(got, []uint64{2, 1}) {
		t.Fatalf("setup: %v", got)
	}

	monitor.drop([]Message{{Channel: "dm:alice@home:bob@home", Sequence: 2}})
	if got := sequencesFound(t, monitor, "deploy"); !slices.Equal(got, []uint64{1}) {
		t.Errorf("after a single delete: %v, want only 1", got)
	}
	monitor.index.dropBelow(2)
	monitor.store.removeBelow(2)
	if got := sequencesFound(t, monitor, "deploy"); len(got) != 0 {
		t.Errorf("after retention: %v, want none", got)
	}
}

func TestANewMessageIsFoundAtOnce(t *testing.T) {
	monitor := ftsMonitor(t)
	monitor.add(Message{Sequence: 9, Channel: "general", Status: Verified, From: "alice@home", Text: "fresh"})
	if got := sequencesFound(t, monitor, "fresh"); !slices.Equal(got, []uint64{9}) {
		t.Errorf("found %v, want the message just added, before the writer's next batch", got)
	}
}

func TestSearchPagesWordsByCursor(t *testing.T) {
	monitor := New(Config{Report: failOnError(t)})
	for sequence := uint64(1); sequence <= 7; sequence++ {
		monitor.add(Message{Sequence: sequence, Channel: "general", Status: Verified, From: "alice@home", Text: "cake", Time: time.Unix(int64(100-sequence), 0)})
	}
	// The newest by sequence comes first, whatever the clocks of the senders say.
	var seen []uint64
	cursor := ""
	for {
		result, err := monitor.Search(context.Background(), mustParse(t, "cake"), 3, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range result.Hits {
			seen = append(seen, hit.Sequence)
		}
		if cursor = result.NextCursor; cursor == "" {
			break
		}
	}
	if !slices.Equal(seen, []uint64{7, 6, 5, 4, 3, 2, 1}) {
		t.Errorf("pages gave %v", seen)
	}
}

func TestSearchWaitsForTheFirstReadOfTheStream(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	for i := uint64(1); i <= 3; i++ {
		fake.store(bus.GeneralChannel(), i*10, alice.sign(t, bus.GeneralChannel(), "cake"))
	}
	monitor := New(Config{Admin: fake, History: fake, Report: failOnError(t)})
	monitor.indexing.Store(true)
	server := serve(monitor)
	defer server.Close()

	_, err := monitor.Search(context.Background(), mustParse(t, "cake"), 10, "")
	var indexing ErrIndexing
	if !errors.As(err, &indexing) || indexing.Total != 3 {
		t.Errorf("search while indexing: %v, want ErrIndexing of 3 messages", err)
	}
	response, body := call(t, server, "GET", "/api/v1/search?q=cake", "", nil)
	if response.StatusCode != http.StatusServiceUnavailable || errorCode(body) != "indexing" {
		t.Errorf("answer %d %s, want 503 indexing", response.StatusCode, body)
	}
}

func TestTheFirstReadEndsWhenEverySourceIsCaughtUp(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 10, alice.sign(t, bus.GeneralChannel(), "cake"))
	monitor := runMonitor(t, fake)
	waitForMessages(t, monitor, "general", 1)
	for deadline := time.Now().Add(5 * time.Second); monitor.indexing.Load(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("still indexing")
		}
	}
	if got := sequencesFound(t, monitor, "cake"); !slices.Equal(got, []uint64{10}) {
		t.Errorf("found %v", got)
	}
}

func TestARestartedMonitorSearchesWhatItStoredWithoutIndexing(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 10, alice.sign(t, bus.GeneralChannel(), "cake today"))
	path := t.TempDir() + "/index.sqlite"
	first, _, stop := storedMonitor(t, fake, path, failOnError(t))
	waitForMessages(t, first, "general", 1)
	stop()

	restarted, _, _ := storedMonitor(t, fake, path, failOnError(t))
	if got := sequencesFound(t, restarted, "cake"); !slices.Equal(got, []uint64{10}) {
		t.Errorf("found %v, want the stored message at once", got)
	}
}

func TestSearchByFiltersAloneMergesChannelsNewestFirst(t *testing.T) {
	monitor := New(Config{Report: failOnError(t)})
	for sequence := uint64(1); sequence <= 9; sequence++ {
		channel := []string{"general", "group:ops", "group:dev"}[sequence%3]
		monitor.add(Message{Sequence: sequence, Channel: channel, Status: Verified, From: "alice@home", Type: "message", Text: "x"})
	}
	var seen []uint64
	cursor := ""
	for {
		result, err := monitor.Search(context.Background(), mustParse(t, "from:alice"), 4, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range result.Hits {
			seen = append(seen, hit.Sequence)
		}
		if cursor = result.NextCursor; cursor == "" {
			break
		}
	}
	if !slices.Equal(seen, []uint64{9, 8, 7, 6, 5, 4, 3, 2, 1}) {
		t.Errorf("pages gave %v", seen)
	}
}

package monitor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, text string) Query {
	t.Helper()
	query, err := ParseQuery(text)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", text, err)
	}
	return query
}

func TestParseQuery(t *testing.T) {
	query := mustParse(t, `Deploy "lib x" from:Alice type:question TYPE:answer has:target after:2026-11-20 http://example.com`)
	words := []string{}
	for _, term := range query.terms {
		words = append(words, string(term))
	}
	if want := []string{"deploy", "lib x", "http://example.com"}; !slices.Equal(words, want) {
		t.Errorf("terms %q, want %q", words, want)
	}
	if !slices.Equal(query.from, []string{"alice"}) || !slices.Equal(query.types, []string{"question", "answer"}) || !query.hasTarget {
		t.Errorf("filters: %+v", query)
	}
	if want := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC); !query.after.Equal(want) {
		t.Errorf("after %v, want %v", query.after, want)
	}
	if got := mustParse(t, `from:"a b@c"`); !slices.Equal(got.from, []string{"a b@c"}) {
		t.Errorf("quoted filter value: %q", got.from)
	}
}

func TestParseQueryRejects(t *testing.T) {
	for _, text := range []string{"", "   ", `""`, "has:everything", "after:yesterday", "before:2026-13-01", strings.Repeat("x", maxQueryRunes+1)} {
		if _, err := ParseQuery(text); err == nil {
			t.Errorf("ParseQuery(%q) succeeded", text)
		}
	}
	if _, err := ParseQuery("type:question"); err != nil {
		t.Errorf("a filter alone is a query: %v", err)
	}
}

func TestMatches(t *testing.T) {
	deploy := Message{
		Channel: "group:ops", From: "Watcher-Sandbox@gentoomaniac", To: "ops", Type: "question", Targets: []string{"bob@home"},
		Title: "Deploy lib-x?", Text: "Can anyone deploy Überlib today? @bob@home", Time: time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC),
	}
	direct := Message{Channel: "dm:alice@home:bob@home", From: "alice@home", To: "bob@home", Type: "message", Text: "back soon", Time: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
	tests := []struct {
		query   string
		message Message
		want    bool
	}{
		{"DEPLOY", deploy, true},
		{"deploy anyone", deploy, true},
		{"deploy nobody", deploy, false},
		{"lib-x", deploy, true},
		{`"anyone deploy"`, deploy, true},
		{`"deploy anyone"`, deploy, false},
		{"überlib", deploy, true},   // case folding beyond ASCII
		{"@bob@home", deploy, true}, // a term that starts with punctuation needs no word start
		{"ack", direct, false},      // matches at word starts only, not inside "back"
		{"back", direct, true},
		{"from:watcher-sandbox", deploy, true}, // a bare name matches the part before the @
		{"from:watcher-sandbox@gentoomaniac", deploy, true},
		{"from:watcher-sandbox@other", deploy, false},
		{"from:watcher", deploy, false},
		{"to:#ops", deploy, true},
		{"to:#general", deploy, false},
		{"to:bob", direct, true},
		{"to:bob", deploy, false}, // to: names a DM recipient, not a target
		{"type:question", deploy, true},
		{"type:answer type:question", deploy, true},
		{"type:answer", deploy, false},
		{"has:target", deploy, true},
		{"has:target", direct, false},
		{"targets:bob", deploy, true},
		{"targets:alice", deploy, false},
		{"after:2026-11-20", deploy, true},
		{"after:2026-11-21", deploy, false},
		{"before:2026-11-20", deploy, false},
		{"before:2026-11-21", deploy, true},
		{"after:2026-11-20t16:00:00z", deploy, false},
		{"deploy from:watcher-sandbox type:question before:2026-12-01", deploy, true},
	}
	for _, test := range tests {
		if got := mustParse(t, test.query).matches(test.message); got != test.want {
			t.Errorf("%q matches %s: %v, want %v", test.query, test.message.Channel, got, test.want)
		}
	}
	if mustParse(t, "x").matches(Message{Channel: "general", Text: "x", Reason: "x"}) {
		t.Error("a message that is no envelope has nothing to find")
	}
}

func TestSnippet(t *testing.T) {
	query := mustParse(t, "needle")
	short := query.snippet(Message{Text: "a Needle here"})
	if short.Field != "text" || short.Text != "a Needle here" || !slices.Equal(short.Matches, [][2]int{{2, 8}}) {
		t.Errorf("short: %+v", short)
	}

	long := query.snippet(Message{Text: strings.Repeat("é", 300) + " needle " + strings.Repeat("x", 300)})
	runes := []rune(long.Text)
	if len(runes) > snippetRunes+2 || runes[0] != '…' || runes[len(runes)-1] != '…' || len(long.Matches) != 1 {
		t.Fatalf("long: %d runes, %v", len(runes), long.Matches)
	}
	if got := string(runes[long.Matches[0][0]:long.Matches[0][1]]); got != "needle" {
		t.Errorf("the offsets mark %q", got)
	}

	if title := query.snippet(Message{Title: "The needle", Text: "nothing"}); title.Field != "title" || title.Text != "The needle" {
		t.Errorf("title: %+v", title)
	}
	if plain := mustParse(t, "type:ack").snippet(Message{Text: "start of it"}); plain.Text != "start of it" || len(plain.Matches) != 0 {
		t.Errorf("no words: %+v", plain)
	}
	emoji := query.snippet(Message{Text: "\U0001f600 needle"})
	if emoji.Matches[0] != [2]int{2, 8} {
		t.Errorf("offsets count code points, not bytes or UTF-16 units: %v", emoji.Matches)
	}
}

// searchServer holds messages 1..count in "general", one a minute, the odd ones about "cake".
func searchServer(t *testing.T, count int) (*Monitor, string) {
	t.Helper()
	monitor := New(Config{})
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= count; i++ {
		text := fmt.Sprintf("message %d", i)
		if i%2 == 1 {
			text += " about cake"
		}
		monitor.add(Message{Sequence: uint64(i), Channel: "general", Status: Verified, From: "alice@home", To: "general", Type: "message", Text: text, Time: start.Add(time.Duration(i) * time.Minute)})
	}
	server := serve(monitor)
	t.Cleanup(server.Close)
	return monitor, server.URL
}

func search(t *testing.T, base string, query string) (int, SearchResult) {
	t.Helper()
	response, err := http.Get(base + "/api/v1/search?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result SearchResult
	json.NewDecoder(response.Body).Decode(&result)
	return response.StatusCode, result
}

func TestSearchPagesNewestFirst(t *testing.T) {
	_, base := searchServer(t, 11) // six messages about cake: 11, 9, 7, 5, 3, 1
	status, first := search(t, base, "q=cake&limit=4")
	if status != http.StatusOK || len(first.Hits) != 4 || first.NextCursor == "" || first.Hits[0].Sequence != 11 || first.Hits[3].Sequence != 5 {
		t.Fatalf("first page: %d %+v", status, first)
	}
	if first.Hits[0].Channel != "general" || first.Hits[0].From != "alice@home" || first.Hits[0].Snippet.Field != "text" || len(first.Hits[0].Snippet.Matches) != 1 {
		t.Errorf("hit: %+v", first.Hits[0])
	}
	_, second := search(t, base, "q=cake&limit=4&cursor="+first.NextCursor)
	if len(second.Hits) != 2 || second.Hits[0].Sequence != 3 || second.Hits[1].Sequence != 1 || second.NextCursor != "" {
		t.Errorf("second page: %+v", second)
	}
}

func TestSearchCursorSurvivesNewMessages(t *testing.T) {
	monitor, base := searchServer(t, 5)
	_, first := search(t, base, "q=cake&limit=2")
	monitor.add(Message{Sequence: 6, Channel: "general", Status: Verified, From: "alice@home", Text: "fresh cake", Time: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)})
	_, second := search(t, base, "q=cake&limit=2&cursor="+first.NextCursor)
	if len(second.Hits) != 1 || second.Hits[0].Sequence != 1 {
		t.Errorf("a message arriving between pages moved the next page: %+v", second)
	}
}

func TestSearchLimits(t *testing.T) {
	_, base := searchServer(t, maxSearchLimit+50)
	if _, result := search(t, base, "q=message&limit=100000"); len(result.Hits) != maxSearchLimit {
		t.Errorf("limit not capped: %d hits", len(result.Hits))
	}
	if _, result := search(t, base, "q=message"); len(result.Hits) != defaultSearchLimit {
		t.Errorf("default limit: %d hits", len(result.Hits))
	}
	if _, result := search(t, base, "q=message&limit=0"); len(result.Hits) != 1 {
		t.Errorf("limit 0 still returns one hit: %d", len(result.Hits))
	}
	for _, bad := range []string{"", "q=", "q=has:x", "q=cake&limit=-1", "q=cake&limit=many", "q=cake&cursor=nonsense", "q=cake&cursor=bm9wZQ"} {
		if status, _ := search(t, base, bad); status != http.StatusBadRequest {
			t.Errorf("search?%s: %d, want 400", bad, status)
		}
	}
	if status, result := search(t, base, "q=zebra"); status != http.StatusOK || result.Hits == nil || len(result.Hits) != 0 {
		t.Errorf("no match: %d %+v", status, result)
	}
}

func TestSearchNeedsLogin(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	response, token := login(t, server, testPassword, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", response.StatusCode)
	}
	if response, body := call(t, server, "GET", "/api/v1/search?q=x", "", nil); response.StatusCode != http.StatusUnauthorized || errorCode(body) != "unauthenticated" {
		t.Errorf("without login: %d %s", response.StatusCode, body)
	}
	if response, _ := call(t, server, "GET", "/api/v1/search?q=x", "", map[string]string{"Authorization": "Bearer " + token}); response.StatusCode != http.StatusOK {
		t.Errorf("with the session: %d", response.StatusCode)
	}
}

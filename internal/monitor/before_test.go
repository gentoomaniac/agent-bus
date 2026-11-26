package monitor

import (
	"context"
	"net/http"
	"testing"
)

func TestMessagesBeforeASequenceAreExactlyAPage(t *testing.T) {
	monitor, _ := lazyMonitor(t, 8) // sequences 10, 20 ... 80: sparse, as a channel is in the stream
	ctx := context.Background()

	page, err := monitor.MessagesBefore(ctx, "general", 65, 3)
	if err != nil || len(page) != 3 || page[0].Sequence != 40 || page[2].Sequence != 60 || page[0].Text != "m4" {
		t.Errorf("3 before 65 = %+v, %v, want 40, 50, 60", page, err)
	}
	if page, _ = monitor.MessagesBefore(ctx, "general", 40, 100); len(page) != 3 || page[0].Sequence != 10 || page[2].Sequence != 30 {
		t.Errorf("before 40 = %+v, want the three older ones", page)
	}
	if page, _ = monitor.MessagesBefore(ctx, "general", 10, 5); len(page) != 0 {
		t.Errorf("before the first = %+v, want none", page)
	}
	if page, _ = monitor.MessagesBefore(ctx, "general", 81, 2); len(page) != 2 || page[1].Sequence != 80 {
		t.Errorf("before last+1 = %+v, want the newest two", page)
	}
	if page, _ = monitor.MessagesBefore(ctx, "nowhere", 81, 2); len(page) != 0 {
		t.Errorf("before in an unknown channel = %+v", page)
	}
}

func TestMessagesEndpointTakesBeforeButNotWithAfter(t *testing.T) {
	monitor, _ := lazyMonitor(t, 8)
	server := serve(monitor)
	defer server.Close()

	var page []Message
	getJSON(t, server, "/api/v1/messages?channel=general&before=50&limit=2", &page)
	if len(page) != 2 || page[0].Sequence != 30 || page[1].Sequence != 40 || page[0].Text != "m3" {
		t.Errorf("page = %+v, want 30 and 40", page)
	}
	for _, bad := range []string{"before=x", "after=10&before=50"} {
		if response, body := call(t, server, "GET", "/api/v1/messages?channel=general&"+bad, "", nil); response.StatusCode != http.StatusBadRequest || errorCode(body) != "bad_request" {
			t.Errorf("%s: %d %s, want 400 bad_request", bad, response.StatusCode, body)
		}
	}
}

package monitor

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultMessageLimit = 100
	maxMessageLimit     = 1000
	eventKeepAlive      = 15 * time.Second // keeps proxies and the browser from closing a quiet event stream
)

//go:embed web/index.html
var web embed.FS

// Status says whether the monitor can send, and why not.
type Status struct {
	Agent   string `json:"agent,omitempty"` // the identity messages are sent as
	CanSend bool   `json:"can_send"`
	Note    string `json:"note,omitempty"` // why it cannot send
}

// Handler serves the JSON API under /api and the event stream. It has no login: it shows all
// traffic and belongs on localhost (docs/monitor.md).
func (monitor *Monitor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, web, "web/index.html") })
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Status()) })
	mux.HandleFunc("GET /api/channels", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Channels()) })
	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Agents()) })
	mux.HandleFunc("GET /api/members", monitor.serveMembers)
	mux.HandleFunc("GET /api/messages", monitor.serveMessages)
	mux.HandleFunc("GET /api/events", monitor.serveEvents)
	return mux
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}

func (monitor *Monitor) serveMembers(w http.ResponseWriter, r *http.Request) {
	members, err := monitor.Members(r.Context(), r.URL.Query().Get("channel"))
	if errors.Is(err, ErrUnknownChannel) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, members)
}

// serveMessages pages by sequence: ?channel=<id>&after=<sequence>&limit=<n>. To get the next page,
// pass the sequence of the last message as after.
func (monitor *Monitor) serveMessages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	after, afterErr := parseUint(query.Get("after"), 0)
	limit, limitErr := parseUint(query.Get("limit"), defaultMessageLimit)
	if err := errors.Join(afterErr, limitErr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	messages := monitor.Messages(query.Get("channel"), after, int(min(limit, maxMessageLimit)))
	if messages == nil {
		messages = []Message{}
	}
	writeJSON(w, messages)
}

func parseUint(text string, fallback uint64) (uint64, error) {
	if text == "" {
		return fallback, nil
	}
	return strconv.ParseUint(text, 10, 64)
}

// serveEvents streams every new message as a server-sent event named "message". The stream ends
// when the client leaves or falls too far behind; the browser then reconnects and reads what it
// missed from /api/messages.
func (monitor *Monitor) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	messages, unsubscribe := monitor.Subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepAlive := time.NewTicker(eventKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		case message, open := <-messages:
			if !open {
				return
			}
			data, err := json.Marshal(message)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		}
		flusher.Flush()
	}
}

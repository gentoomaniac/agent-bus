package monitor

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

const (
	defaultMessageLimit = 100
	maxMessageLimit     = 1000
	maxSendBytes        = 2 * envelope.MaxRecordBytes // the body limit is enforced when the message is signed
	eventKeepAlive      = 15 * time.Second            // keeps proxies and the browser from closing a quiet event stream
)

//go:embed web/index.html
var web embed.FS

// The states of the monitor's connection to the bus.
const (
	BusLive         = "live"
	BusReconnecting = "reconnecting" // the broker is down or unreachable; the monitor keeps trying
)

// Status says whether the monitor reaches the bus, whether it can send, and why not.
type Status struct {
	Bus     string `json:"bus"`
	Agent   string `json:"agent,omitempty"` // the identity messages are sent as
	CanSend bool   `json:"can_send"`
	Note    string `json:"note,omitempty"` // why it cannot send
}

// Handler serves the JSON API under /api/v1 and the event stream. It has no login: it shows all
// traffic and belongs on localhost or behind a reverse proxy (docs/monitor.md). It answers only
// requests whose Host header access accepts.
func (monitor *Monitor) Handler(access Access) (http.Handler, error) {
	if err := access.Validate(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, web, "web/index.html") })
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Status()) })
	mux.HandleFunc("GET /api/v1/channels", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Channels()) })
	mux.HandleFunc("GET /api/v1/agents", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Agents()) })
	mux.HandleFunc("GET /api/v1/presence", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Presences()) })
	mux.HandleFunc("GET /api/v1/members", monitor.serveMembers)
	mux.HandleFunc("GET /api/v1/messages", monitor.serveMessages)
	mux.HandleFunc("GET /api/v1/events", monitor.serveEvents)
	mux.HandleFunc("POST /api/v1/send", monitor.serveSend)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !access.acceptsHost(access.clientOf(r).hostHeader) {
			writeError(w, http.StatusForbidden, "forbidden_host", "unexpected Host header")
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// writeError answers with the API's one error shape: a message for people and a stable code for programs.
func writeError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message, "code": code})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}

func (monitor *Monitor) serveMembers(w http.ResponseWriter, r *http.Request) {
	members, err := monitor.Members(r.Context(), r.URL.Query().Get("channel"))
	if errors.Is(err, ErrUnknownChannel) {
		writeError(w, http.StatusNotFound, "unknown_channel", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "bus_unavailable", err.Error())
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
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
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

// serveSend posts a message and answers {"id": ...}. It insists on a JSON content type, which a
// page on another origin cannot send without the browser asking this server first.
func (monitor *Monitor) serveSend(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "send wants Content-Type: application/json")
		return
	}
	var request SendRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSendBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := monitor.Send(r.Context(), request)
	switch {
	case errors.Is(err, ErrReadOnly):
		writeError(w, http.StatusForbidden, "read_only", err.Error())
	case errors.Is(err, ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case err != nil:
		writeError(w, http.StatusBadGateway, "bus_unavailable", err.Error())
	default:
		writeJSON(w, map[string]string{"id": id})
	}
}

// serveEvents streams every new message as a server-sent event named "message" and every presence
// change as one named "presence". The stream ends when the client leaves or falls too far behind;
// the browser then reconnects and reads what it missed from /api/v1/messages and /api/v1/presence.
func (monitor *Monitor) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming is not supported")
		return
	}
	messages, unsubscribe := monitor.Subscribe()
	defer unsubscribe()
	views, unsubscribeViews := monitor.SubscribePresence()
	defer unsubscribeViews()
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
		case view, open := <-views:
			if !open {
				return
			}
			data, err := json.Marshal(view)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "event: presence\ndata: %s\n\n", data)
		}
		flusher.Flush()
	}
}

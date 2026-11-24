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
)

// eventKeepAlive keeps proxies and the browser from closing a quiet event stream. It is also when
// a stream notices that its login session has ended. A variable for the tests.
var eventKeepAlive = 15 * time.Second

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

// Handler serves the page and the JSON API under /api/v1. Everything but the page and the login
// needs the password when access has one (docs/api.md). It answers only requests whose Host header
// access accepts.
func (monitor *Monitor) Handler(access Access) (http.Handler, error) {
	return monitor.handler(access, time.Now)
}

func (monitor *Monitor) handler(access Access, now func() time.Time) (http.Handler, error) {
	if err := access.Validate(); err != nil {
		return nil, err
	}
	login := gate{access: access, auth: newAuth(access.PasswordHash, now)}
	read := func(handler http.HandlerFunc) http.HandlerFunc { return login.guard(false, handler) }
	change := func(handler http.HandlerFunc) http.HandlerFunc { return login.guard(true, handler) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, web, "web/index.html") })
	mux.HandleFunc("POST /api/v1/login", login.serveLogin)
	mux.HandleFunc("POST /api/v1/logout", change(login.serveLogout))
	mux.HandleFunc("GET /api/v1/status", read(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, struct {
			Status
			PasswordSet bool `json:"password_set"`
		}{monitor.Status(), login.auth.enabled()})
	}))
	mux.HandleFunc("GET /api/v1/channels", read(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Channels()) }))
	mux.HandleFunc("GET /api/v1/agents", read(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Agents()) }))
	mux.HandleFunc("GET /api/v1/presence", read(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, monitor.Presences()) }))
	mux.HandleFunc("GET /api/v1/members", read(monitor.serveMembers))
	mux.HandleFunc("GET /api/v1/messages", read(monitor.serveMessages))
	mux.HandleFunc("GET /api/v1/search", read(monitor.serveSearch))
	mux.HandleFunc("GET /api/v1/events", read(func(w http.ResponseWriter, r *http.Request) {
		who := principalOf(r)
		monitor.serveEvents(w, r, func() bool { return who.token == "" || login.auth.isLive(who.token) })
	}))
	mux.HandleFunc("POST /api/v1/send", change(monitor.serveSend))
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

// serveSearch answers ?q=<query>&limit=<n>&cursor=<cursor>, newest first. The query syntax is in
// docs/api.md; limit is cut to maxSearchLimit and the cursor is the previous page's next_cursor.
func (monitor *Monitor) serveSearch(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	limit, limitErr := parseUint(params.Get("limit"), defaultSearchLimit)
	query, queryErr := ParseQuery(params.Get("q"))
	if err := errors.Join(limitErr, queryErr); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	result, err := monitor.Search(query, int(max(1, min(limit, maxSearchLimit))), params.Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, result)
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
// change as one named "presence". The stream ends when the client leaves, falls too far behind or
// its login session ends; the browser then reconnects and reads what it missed from
// /api/v1/messages and /api/v1/presence. Events carry an id, but nothing is replayed after a
// reconnect: Last-Event-ID is ignored, the client re-reads the state (snapshot and reconnect).
func (monitor *Monitor) serveEvents(w http.ResponseWriter, r *http.Request, loggedIn func() bool) {
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
			if !loggedIn() {
				return
			}
			fmt.Fprint(w, ": keep-alive\n\n")
		case message, open := <-messages:
			if !open || !loggedIn() {
				return
			}
			data, err := json.Marshal(message)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "id: %s#%d\nevent: message\ndata: %s\n\n", message.Channel, message.Sequence, data)
		case view, open := <-views:
			if !open || !loggedIn() {
				return
			}
			data, err := json.Marshal(view)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "id: presence:%s@%d\nevent: presence\ndata: %s\n\n", view.Agent, time.Now().UnixNano(), data)
		}
		flusher.Flush()
	}
}

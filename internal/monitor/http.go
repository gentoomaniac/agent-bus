package monitor

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

const (
	defaultMessageLimit = 100
	maxMessageLimit     = 1000
	maxSendBytes        = 2 * envelope.MaxRecordBytes // the body limit is enforced when the message is signed
	// maxChannelChangeBytes bounds a channel change's body: busd reads at most this much of a request.
	maxChannelChangeBytes = channelapi.MaxRequestBytes
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
	// PendingEnrollments is how many enrollment requests wait for the owner, as busd last
	// announced (docs/design/busd.md). The monitor learns the number and nothing else.
	PendingEnrollments int `json:"pending_enrollments,omitempty"`
	// Directory is how the monitor follows the directory (docs/design/directory-v2.md): busd's
	// last announced version and a failure to read the registry or the catalogue.
	Directory DirectoryStatus `json:"directory"`
	// Search is "indexing" while the word search is not ready, "index_failed" when writing the
	// state file failed so that it misses new messages; absent when it works.
	Search string `json:"search,omitempty"`
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
	mux.HandleFunc("GET /api/v1/messages/by-id", read(monitor.serveMessageByID))
	mux.HandleFunc("GET /api/v1/messages/raw", read(monitor.serveRawMessage))
	mux.HandleFunc("GET /api/v1/search", read(monitor.serveSearch))
	mux.HandleFunc("GET /api/v1/events", read(func(w http.ResponseWriter, r *http.Request) {
		who := principalOf(r)
		monitor.serveEvents(w, r, func() bool { return who.token == "" || login.auth.isLive(who.token) })
	}))
	mux.HandleFunc("POST /api/v1/send", change(monitor.serveSend))
	mux.HandleFunc("POST /api/v1/channels/{action}", change(monitor.serveChannelChange))
	mux.HandleFunc("GET /api/v1/prefs", read(monitor.servePrefs))
	mux.HandleFunc("POST /api/v1/prefs/read", change(monitor.servePrefsRead))
	mux.HandleFunc("PUT /api/v1/prefs/favourites", change(monitor.servePrefsFavourites))
	mux.HandleFunc("PUT /api/v1/prefs/subscribed", change(monitor.servePrefsSubscribed))
	mux.HandleFunc("PUT /api/v1/prefs/collapsed", change(monitor.servePrefsCollapsed))
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

// writeReadError answers a failed read of messages: 404 for one that does not exist, 503 when the
// stream could not be read, which a client may try again.
func writeReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnknownMessage) {
		writeError(w, http.StatusNotFound, "unknown_message", err.Error())
		return
	}
	if errors.Is(err, ErrIndexFailed) {
		writeError(w, http.StatusServiceUnavailable, "index_failed", err.Error())
		return
	}
	if errors.As(err, new(ErrIndexing)) {
		writeError(w, http.StatusServiceUnavailable, "indexing", err.Error())
		return
	}
	writeError(w, http.StatusServiceUnavailable, "bus_unavailable", err.Error())
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

// serveMessages pages by sequence. ?channel=<id>&after=<sequence>&limit=<n> goes forward: for the
// next page pass the sequence of the last message as after. ?channel=<id>&before=<sequence>&limit=<n>
// goes back: for the page before pass the sequence of the first message as before.
func (monitor *Monitor) serveMessages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	after, afterErr := parseUint(query.Get("after"), 0)
	before, beforeErr := parseUint(query.Get("before"), 0)
	limit, limitErr := parseUint(query.Get("limit"), defaultMessageLimit)
	err := errors.Join(afterErr, beforeErr, limitErr)
	if query.Has("after") && query.Has("before") {
		err = errors.Join(err, errors.New("after and before cannot be combined"))
	}
	// An empty before is a client that lost its cursor; paging back from sequence 0 would answer
	// "no older message" and hide that.
	if query.Has("before") && query.Get("before") == "" {
		err = errors.Join(err, errors.New("before needs a sequence"))
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	channel, count := query.Get("channel"), int(min(limit, maxMessageLimit))
	var messages []Message
	if query.Has("before") {
		messages, err = monitor.MessagesBefore(r.Context(), channel, before, count)
	} else {
		messages, err = monitor.Messages(r.Context(), channel, after, count)
	}
	if err != nil {
		writeReadError(w, err)
		return
	}
	if messages == nil {
		messages = []Message{}
	}
	writeJSON(w, messages)
}

// serveMessageByID answers ?id=<message id> with the message, as /messages lists it, wherever it is.
func (monitor *Monitor) serveMessageByID(w http.ResponseWriter, r *http.Request) {
	message, err := monitor.MessageByID(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		writeReadError(w, err)
		return
	}
	writeJSON(w, message)
}

// serveRawMessage answers ?channel=<id>&sequence=<n> with the record as stored, parsed as JSON,
// or as text when the bytes are not JSON.
func (monitor *Monitor) serveRawMessage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sequence, err := strconv.ParseUint(query.Get("sequence"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "sequence: "+err.Error())
		return
	}
	message, stored, err := monitor.RawMessage(r.Context(), query.Get("channel"), sequence)
	if err != nil {
		writeReadError(w, err)
		return
	}
	answer := struct {
		Channel  string          `json:"channel"`
		Sequence uint64          `json:"sequence"`
		Status   Verification    `json:"status"`
		Reason   string          `json:"reason,omitempty"`
		Size     int             `json:"size"`
		Raw      json.RawMessage `json:"raw,omitempty"`
		RawText  string          `json:"raw_text,omitempty"`
	}{Channel: message.Channel, Sequence: message.Sequence, Status: message.Status, Reason: message.Reason, Size: len(stored)}
	if json.Valid(stored) {
		answer.Raw = stored
	} else {
		answer.RawText = string(stored)
	}
	writeJSON(w, answer)
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
	result, err := monitor.Search(r.Context(), query, int(max(1, min(limit, maxSearchLimit))), params.Get("cursor"))
	if errors.Is(err, ErrBadCursor) {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err != nil {
		writeReadError(w, err)
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

// serveChannelChange carries out POST /api/v1/channels/<action> with a ChannelChange as its body
// and answers with the channel as it is now. Like send, it insists on a JSON content type.
func (monitor *Monitor) serveChannelChange(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "channel changes want Content-Type: application/json")
		return
	}
	var change ChannelChange
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChannelChangeBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&change); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	changed, err := monitor.ChangeChannel(r.Context(), r.PathValue("action"), change)
	switch {
	case errors.Is(err, ErrReadOnly):
		writeError(w, http.StatusForbidden, "read_only", err.Error())
	case errors.Is(err, ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrRefused):
		writeError(w, http.StatusConflict, "refused", err.Error())
	case err != nil:
		writeError(w, http.StatusBadGateway, "bus_unavailable", err.Error())
	default:
		writeJSON(w, changed)
	}
}

// serveEvents streams every new message as a server-sent event named "message", every presence
// change as one named "presence" and the full prefs after every change as one named "prefs". The
// stream ends when the client leaves, falls too far behind or its login session ends; the browser
// then reconnects and reads what it missed from /api/v1/messages, /api/v1/presence and
// /api/v1/prefs. Events carry an id, but nothing is replayed after a reconnect: Last-Event-ID is
// ignored, the client re-reads the state (snapshot and reconnect).
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
	prefs, unsubscribePrefs := monitor.SubscribePrefs()
	defer unsubscribePrefs()
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
		case changed, open := <-prefs:
			if !open || !loggedIn() {
				return
			}
			data, err := json.Marshal(changed)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "id: prefs@%d\nevent: prefs\ndata: %s\n\n", time.Now().UnixNano(), data)
		}
		flusher.Flush()
	}
}

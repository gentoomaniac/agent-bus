package monitor

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
	maxQueryRunes      = 500
	snippetRunes       = 160 // a snippet is cut to about this many runes around the first match
)

// Query is a parsed search: words and phrases that must all appear in the title or text, and
// filters that must all hold. Several type: filters are alternatives.
type Query struct {
	terms     [][]rune // lower-cased
	from      []string // agent IDs or bare agent names, lower-cased
	to        []string // "#channel", an agent ID or a bare agent name, lower-cased
	types     []string
	targets   []string
	hasTarget bool
	after     time.Time // on or after; zero for any
	before    time.Time // strictly before; zero for any
}

// ParseQuery reads the search syntax: words, "quoted phrases" and key:value filters (from, to,
// type, targets, has:target, after, before). A word with an unknown key, such as a URL, is an
// ordinary word. A query needs at least one word or filter.
func ParseQuery(text string) (Query, error) {
	var query Query
	if len([]rune(text)) > maxQueryRunes {
		return query, fmt.Errorf("the query is longer than %d characters", maxQueryRunes)
	}
	for _, token := range tokenize(text) {
		key, value, isFilter := strings.Cut(token.text, ":")
		key = strings.ToLower(key)
		if token.quoted || !isFilter || value == "" || !slices.Contains(filterKeys, key) {
			if term := lowerRunes(token.text); len(term) > 0 {
				query.terms = append(query.terms, term)
			}
			continue
		}
		value = strings.ToLower(value)
		switch key {
		case "from":
			query.from = append(query.from, value)
		case "to":
			query.to = append(query.to, value)
		case "type":
			query.types = append(query.types, value)
		case "targets":
			query.targets = append(query.targets, value)
		case "has":
			if value != "target" {
				return query, fmt.Errorf("has: knows only target, not %q", value)
			}
			query.hasTarget = true
		case "after", "before":
			day, err := parseSearchTime(value)
			if err != nil {
				return query, fmt.Errorf("%s: %w", key, err)
			}
			if key == "after" {
				query.after = day
			} else {
				query.before = day
			}
		}
	}
	if len(query.terms) == 0 && !query.hasFilter() {
		return query, errors.New("the query is empty")
	}
	return query, nil
}

var filterKeys = []string{"from", "to", "type", "targets", "has", "after", "before"}

func (query Query) hasFilter() bool {
	return len(query.from)+len(query.to)+len(query.types)+len(query.targets) > 0 ||
		query.hasTarget || !query.after.IsZero() || !query.before.IsZero()
}

// parseSearchTime reads a day (YYYY-MM-DD, midnight UTC) or an RFC 3339 time. The zero time
// (0001-01-01) is refused: Query uses it for "no filter", so before:0001-01-01 would match
// everything instead of nothing.
func parseSearchTime(text string) (time.Time, error) {
	moment, err := time.Parse(time.DateOnly, text)
	if err != nil {
		moment, err = time.Parse(time.RFC3339, strings.ToUpper(text))
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither YYYY-MM-DD nor an RFC 3339 time", text)
	}
	if moment.IsZero() {
		return time.Time{}, fmt.Errorf("%q is not a usable time", text)
	}
	return moment, nil
}

type token struct {
	text   string
	quoted bool
}

// tokenize splits on white space; a double-quoted run is one token, also after a key: as in
// from:"a b". An unclosed quote runs to the end.
func tokenize(text string) []token {
	var tokens []token
	var current strings.Builder
	quoted, inQuotes, started := false, false, false
	flush := func() {
		if started {
			tokens = append(tokens, token{current.String(), quoted})
		}
		current.Reset()
		quoted, started = false, false
	}
	for _, r := range text {
		switch {
		case r == '"':
			if !inQuotes && current.Len() == 0 {
				quoted = true
			}
			inQuotes, started = !inQuotes, true
		case unicode.IsSpace(r) && !inQuotes:
			flush()
		default:
			current.WriteRune(r)
			started = true
		}
	}
	flush()
	return tokens
}

func lowerRunes(text string) []rune {
	runes := []rune(text)
	for i, r := range runes {
		runes[i] = unicode.ToLower(r)
	}
	return runes
}

// SearchHit is one message that matched, with the part of its text that shows why.
type SearchHit struct {
	Channel  string    `json:"channel"`
	Sequence uint64    `json:"sequence"`
	ID       string    `json:"id,omitempty"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	Type     string    `json:"type,omitempty"`
	Targets  []string  `json:"targets,omitempty"`
	Time     time.Time `json:"time,omitzero"`
	Title    string    `json:"title,omitempty"`
	Snippet  Snippet   `json:"snippet"`
}

// Snippet is a piece of the text (or, when only the title matched, of the title) around the
// first match. Matches are [start, end) offsets in Unicode code points of Text; Text starts or
// ends with "…" where it was cut.
type Snippet struct {
	Field   string   `json:"field"` // "text" or "title"
	Text    string   `json:"text"`
	Matches [][2]int `json:"matches,omitempty"`
}

// SearchResult is a page of hits, newest first. NextCursor, if set, fetches the page after it.
type SearchResult struct {
	Hits       []SearchHit `json:"results"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// ErrBadCursor is returned for a cursor this monitor did not hand out.
var ErrBadCursor = errors.New("bad cursor")

// ErrIndexing is returned by Search while the monitor reads the whole stream into a new state
// file: the words of the messages not read yet cannot be found.
type ErrIndexing struct {
	Indexed, Total int // messages read, and in the stream when that is known
}

func (err ErrIndexing) Error() string {
	return fmt.Sprintf("search is not ready, the monitor is indexing the stream (%d of %d messages)", err.Indexed, err.Total)
}

// ErrIndexFailed is returned by a search for words after writing the state file failed: the
// index of words misses every message since, and only a restart catches up.
var ErrIndexFailed = errors.New("search for words is unavailable, the search index could not be written; restart the monitor to rebuild it")

// Search returns up to limit messages matching the query, newest first (by sequence, which is the
// order the bus took them in), below the cursor. Words go to the FTS index of the state file,
// which names the messages; the filters are checked against the index rows. A query with no words
// walks the rows and never touches the file. Bodies are read only for the page's hits.
func (monitor *Monitor) Search(ctx context.Context, query Query, limit int, cursor string) (SearchResult, error) {
	below, err := parseCursor(cursor)
	if err != nil {
		return SearchResult{}, err
	}
	if monitor.indexing.Load() {
		return SearchResult{}, monitor.indexingProgress(ctx)
	}

	var candidates []Message
	if len(query.terms) > 0 {
		if failure := monitor.store.failure(); failure != nil {
			return SearchResult{}, fmt.Errorf("%w (%v)", ErrIndexFailed, failure)
		}
		candidates, err = monitor.searchWords(ctx, query, limit+1, below)
	} else {
		candidates = monitor.searchRows(query, limit+1, below)
	}
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Hits: []SearchHit{}}
	if len(candidates) > limit {
		candidates = candidates[:limit]
		result.NextCursor = cursorAfter(candidates[limit-1].Sequence)
	}
	hits, err := monitor.withBodies(ctx, candidates, false)
	if err != nil {
		return SearchResult{}, err
	}
	for _, message := range hits {
		result.Hits = append(result.Hits, SearchHit{
			Channel: message.Channel, Sequence: message.Sequence, ID: message.ID, From: message.From, To: message.To,
			Type: message.Type, Targets: message.Targets, Time: message.Time, Title: message.Title, Snippet: query.snippet(message),
		})
	}
	return result, nil
}

// searchWords takes the matches of the words from the newest down, until n of them pass the
// filters. Messages the index no longer has (retention is ahead of the file) are skipped.
func (monitor *Monitor) searchWords(ctx context.Context, query Query, n int, below uint64) ([]Message, error) {
	expression, searchable := query.matchExpression()
	if !searchable {
		return nil, nil
	}
	var found []Message
	err := monitor.store.eachMatch(ctx, expression, below, func(channel string, sequence uint64) bool {
		monitor.mu.Lock()
		defer monitor.mu.Unlock()
		if entry, known := monitor.index.find(channel, sequence); known {
			if meta := monitor.index.describe(channel, entry); query.matchesFilters(meta) {
				found = append(found, meta)
			}
		}
		return len(found) < n
	})
	return found, err
}

// searchRows takes the n newest rows below the sequence that pass the filters. The n newest of
// all channels are among the n newest of each, so each channel is walked from its end only as far
// as it takes to find n.
func (monitor *Monitor) searchRows(query Query, n int, below uint64) []Message {
	var found []Message
	monitor.mu.Lock()
	for channel, rows := range monitor.index.channels {
		end, _ := slices.BinarySearchFunc(rows, below, compareRowSequence)
		taken := 0
		for at := end - 1; at >= 0 && taken < n; at-- {
			if meta := monitor.index.describe(channel, rows[at]); query.matchesFilters(meta) {
				found = append(found, meta)
				taken++
			}
		}
	}
	monitor.mu.Unlock()
	slices.SortFunc(found, func(a, b Message) int { return cmp.Compare(b.Sequence, a.Sequence) })
	return found[:min(n, len(found))]
}

// indexingProgress says how far the first read of the stream is.
func (monitor *Monitor) indexingProgress(ctx context.Context) ErrIndexing {
	monitor.mu.Lock()
	progress := ErrIndexing{Indexed: monitor.index.countThrough(math.MaxUint64)}
	monitor.mu.Unlock()
	if state, err := monitor.config.History.LogState(ctx); err == nil {
		progress.Total = int(state.Messages)
	}
	return progress
}

// matchExpression is the query's words as an FTS5 MATCH expression over the title and the text.
// Each word or phrase becomes the phrase of its tokens with the last one a prefix, which is how
// "ack" finds "ack it" and not "back", and "lib-x" finds "lib x". It says false for a word with no
// letter or digit, which the index has no token for.
func (query Query) matchExpression() (string, bool) {
	var phrases []string
	for _, term := range query.terms {
		tokens := strings.FieldsFunc(string(term), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(tokens) == 0 {
			return "", false
		}
		phrases = append(phrases, `"`+strings.Join(tokens, " ")+`" *`)
	}
	return "{title text} : (" + strings.Join(phrases, " AND ") + ")", true
}

// The cursor is the sequence of the last message of a page; the next page is below it.
func cursorAfter(sequence uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(sequence, 10)))
}

// parseCursor returns the sequence the page lies below; the first page lies below all of them.
func parseCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return math.MaxInt64, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrBadCursor
	}
	sequence, err := strconv.ParseUint(string(raw), 10, 63)
	if err != nil {
		return 0, ErrBadCursor
	}
	return sequence, nil
}

// matchesFilters checks what the index knows of a message: its sender, recipient, type, targets and time.
func (query Query) matchesFilters(message Message) bool {
	if message.From == "" { // not a valid envelope: nothing to search
		return false
	}
	if !query.after.IsZero() && message.Time.Before(query.after) || !query.before.IsZero() && !message.Time.Before(query.before) {
		return false
	}
	if len(query.from) > 0 && !slices.ContainsFunc(query.from, func(agent string) bool { return isAgent(message.From, agent) }) {
		return false
	}
	if len(query.to) > 0 && !slices.ContainsFunc(query.to, message.isSentTo) {
		return false
	}
	if len(query.types) > 0 && !slices.Contains(query.types, strings.ToLower(message.Type)) {
		return false
	}
	if query.hasTarget && len(message.Targets) == 0 {
		return false
	}
	for _, agent := range query.targets {
		if !slices.ContainsFunc(message.Targets, func(target string) bool { return isAgent(target, agent) }) {
			return false
		}
	}
	return true
}

// isAgent says whether the ID is the agent, given as a full ID or as the name before the "@".
// The wanted agent is lower-case.
func isAgent(id string, wanted string) bool {
	id = strings.ToLower(id)
	if strings.Contains(wanted, "@") || id == wanted {
		return id == wanted
	}
	name, _, _ := strings.Cut(id, "@")
	return name == wanted
}

// isSentTo matches a "#channel" against the channel the message is in and anything else against
// the recipient of a direct message. The destination is lower-case.
func (message Message) isSentTo(destination string) bool {
	isDM := strings.HasPrefix(message.Channel, "dm:")
	if name, isChannel := strings.CutPrefix(destination, "#"); isChannel {
		return !isDM && strings.EqualFold(strings.TrimPrefix(message.Channel, "group:"), name)
	}
	return isDM && isAgent(message.To, destination)
}

// findTerm returns the [start, end) offsets of the term in the lower-cased text. A term that
// starts with a letter or digit matches only at the start of a word, so "ack" finds "ack it"
// and not "back".
func findTerm(text []rune, term []rune) [][2]int {
	var spans [][2]int
	needsBoundary := isWordRune(term[0])
	for start := 0; start+len(term) <= len(text); start++ {
		if needsBoundary && start > 0 && isWordRune(text[start-1]) {
			continue
		}
		if slices.Equal(text[start:start+len(term)], term) {
			spans = append(spans, [2]int{start, start + len(term)})
			start += len(term) - 1
		}
	}
	return spans
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// snippet shows the text around the first match, or the title when only that matched, or the
// start of the text when the query has no words or the index matched what the snippet cannot
// find, such as "uber" for "Über" (the index folds diacritics).
func (query Query) snippet(message Message) Snippet {
	fields := []struct{ name, content string }{{"text", message.Text}, {"title", message.Title}}
	for _, field := range fields {
		runes, lowered := []rune(field.content), lowerRunes(field.content)
		var spans [][2]int
		for _, term := range query.terms {
			spans = append(spans, findTerm(lowered, term)...)
		}
		if len(spans) > 0 {
			return cutSnippet(field.name, runes, spans)
		}
	}
	for _, field := range fields {
		if field.content != "" {
			return cutSnippet(field.name, []rune(field.content), nil)
		}
	}
	return Snippet{Field: "text"}
}

// cutSnippet keeps about snippetRunes runes, starting a little before the first span.
func cutSnippet(field string, runes []rune, spans [][2]int) Snippet {
	slices.SortFunc(spans, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	start := 0
	if len(spans) > 0 {
		start = max(0, spans[0][0]-snippetRunes/4)
	}
	end := min(len(runes), start+snippetRunes)

	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	snippet := Snippet{Field: field, Text: prefix + string(runes[start:end]) + suffix}
	shift := len([]rune(prefix)) - start
	for _, span := range spans {
		if span[0] >= start && span[1] <= end {
			snippet.Matches = append(snippet.Matches, [2]int{span[0] + shift, span[1] + shift})
		}
	}
	return snippet
}

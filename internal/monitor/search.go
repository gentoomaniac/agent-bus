package monitor

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Search scans the index for the messages that fit the filters and reads their bodies, newest
// first, until a page of them also holds the words. That reads many bodies from the stream on a
// big history and is slow there; a SQLite FTS index replaces the scan without changing the
// endpoint (docs/design/lazy-monitor.md).

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

// parseSearchTime reads a day (YYYY-MM-DD, midnight UTC) or an RFC 3339 time.
func parseSearchTime(text string) (time.Time, error) {
	if day, err := time.Parse(time.DateOnly, text); err == nil {
		return day, nil
	}
	if moment, err := time.Parse(time.RFC3339, strings.ToUpper(text)); err == nil {
		return moment, nil
	}
	return time.Time{}, fmt.Errorf("%q is neither YYYY-MM-DD nor an RFC 3339 time", text)
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

// searchBatch is how many candidates Search reads the bodies of at once.
const searchBatch = 100

// place is where a message stands in the newest-first order of a search.
type place struct {
	time     int64 // Unix nanoseconds
	channel  string
	sequence uint64
}

func placeOf(message Message) place {
	return place{message.Time.UnixNano(), message.Channel, message.Sequence}
}

// Search returns up to limit messages matching the query, newest first, after the cursor.
func (monitor *Monitor) Search(ctx context.Context, query Query, limit int, cursor string) (SearchResult, error) {
	after, err := parseCursor(cursor)
	if err != nil {
		return SearchResult{}, err
	}
	type candidate struct {
		channel string
		entry   row
	}
	var candidates []candidate
	monitor.mu.Lock()
	for channel, rows := range monitor.index.channels {
		for _, entry := range rows {
			meta := monitor.index.describe(channel, entry)
			if query.matchesFilters(meta) && (cursor == "" || newerFirst(placeOf(meta), after) > 0) {
				candidates = append(candidates, candidate{channel, entry})
			}
		}
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		return newerFirst(place{a.entry.time, a.channel, a.entry.sequence}, place{b.entry.time, b.channel, b.entry.sequence})
	})
	monitor.mu.Unlock()

	result := SearchResult{Hits: []SearchHit{}}
	var hits []Message
	for start := 0; start < len(candidates) && len(hits) <= limit; start += searchBatch {
		monitor.mu.Lock()
		var described []Message
		for _, c := range candidates[start:min(len(candidates), start+searchBatch)] {
			described = append(described, monitor.index.describe(c.channel, c.entry))
		}
		monitor.mu.Unlock()
		messages, err := monitor.withBodies(ctx, described)
		if err != nil {
			return SearchResult{}, err
		}
		for _, message := range messages {
			if query.matchesWords(message) {
				hits = append(hits, message)
			}
		}
	}
	if len(hits) > limit {
		hits = hits[:limit]
		result.NextCursor = cursorAfter(hits[limit-1])
	}
	for _, message := range hits {
		result.Hits = append(result.Hits, SearchHit{
			Channel: message.Channel, Sequence: message.Sequence, ID: message.ID, From: message.From, To: message.To,
			Type: message.Type, Targets: message.Targets, Time: message.Time, Title: message.Title, Snippet: query.snippet(message),
		})
	}
	return result, nil
}

// newerFirst orders places newest first; it is negative when a comes before b. The time decides,
// then the channel and sequence make the order total.
func newerFirst(a, b place) int {
	return cmp.Or(cmp.Compare(b.time, a.time), cmp.Compare(a.channel, b.channel), cmp.Compare(b.sequence, a.sequence))
}

// The cursor is the last message of a page; the next page starts after it in the order.
func cursorAfter(last Message) string {
	text := fmt.Sprintf("%d|%d|%s", last.Time.UnixNano(), last.Sequence, last.Channel)
	return base64.RawURLEncoding.EncodeToString([]byte(text))
}

func parseCursor(cursor string) (place, error) {
	if cursor == "" {
		return place{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return place{}, ErrBadCursor
	}
	nanos, rest, _ := strings.Cut(string(raw), "|")
	sequence, channel, _ := strings.Cut(rest, "|")
	unixNano, nanosErr := strconv.ParseInt(nanos, 10, 64)
	number, sequenceErr := strconv.ParseUint(sequence, 10, 64)
	if err := errors.Join(nanosErr, sequenceErr); err != nil || channel == "" {
		return place{}, ErrBadCursor
	}
	return place{unixNano, channel, number}, nil
}

// matches says whether the message satisfies the whole query.
func (query Query) matches(message Message) bool {
	return query.matchesFilters(message) && query.matchesWords(message)
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

// matchesWords checks that every word and phrase of the query is in the title or text.
func (query Query) matchesWords(message Message) bool {
	title, text := lowerRunes(message.Title), lowerRunes(message.Text)
	for _, term := range query.terms {
		if len(findTerm(title, term)) == 0 && len(findTerm(text, term)) == 0 {
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
// start of the text when the query has no words.
func (query Query) snippet(message Message) Snippet {
	for _, field := range []struct{ name, content string }{{"text", message.Text}, {"title", message.Title}} {
		runes, lowered := []rune(field.content), lowerRunes(field.content)
		var spans [][2]int
		for _, term := range query.terms {
			spans = append(spans, findTerm(lowered, term)...)
		}
		if len(spans) > 0 || len(query.terms) == 0 && field.content != "" {
			return cutSnippet(field.name, runes, spans)
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

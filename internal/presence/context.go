package presence

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Context is how much of its context window a session used at its latest model reply: the tokens
// that reply was given, as input, cache reads and cache writes. The window's size is not in the
// transcript, so it is not part of this.
type Context struct {
	Tokens int       `json:"tokens"`
	Model  string    `json:"model,omitempty"`
	At     time.Time `json:"at,omitzero"` // of the reply
}

const (
	contextTail    = 256 << 10 // the end of a transcript read first
	contextTailMax = 8 << 20   // the most read before giving up: long tool results sit in between
)

// transcriptLine is the part of a Claude Code transcript line that the context comes from. Nothing
// else is decoded: prompts, replies and tool results never leave the transcript.
type transcriptLine struct {
	Type      string    `json:"type"`
	Subtype   string    `json:"subtype"`
	Timestamp time.Time `json:"timestamp"`
	Message   *struct {
		Model string `json:"model"`
		Usage *struct {
			Input         int `json:"input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// syntheticModel marks a reply Claude Code wrote itself (an API error, an interruption): it says
// nothing about the context.
const syntheticModel = "<synthetic>"

// ReadContext finds the context of the latest model reply in a Claude Code transcript. It says
// false when the file cannot be read, holds no reply yet, or was compacted after its latest reply:
// the size before the compaction would mislead until the next reply.
func ReadContext(path string) (Context, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Context{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Context{}, false
	}
	size := info.Size()
	for tail := int64(contextTail); ; tail *= 2 {
		start := max(0, size-tail)
		chunk := make([]byte, size-start)
		if _, err := file.ReadAt(chunk, start); err != nil && err != io.EOF {
			return Context{}, false
		}
		if start > 0 { // the first line is cut: it begins before the chunk
			_, chunk, _ = bytes.Cut(chunk, []byte("\n"))
		}
		if context, found, decided := latestContext(chunk); decided {
			return context, found
		}
		if start == 0 || tail >= contextTailMax {
			return Context{}, false
		}
	}
}

// latestContext reads the lines from the last to the first. decided is false when no line said
// anything, so an earlier part of the file has to be read.
func latestContext(chunk []byte) (context Context, found, decided bool) {
	for len(chunk) > 0 {
		var line []byte
		if i := bytes.LastIndexByte(bytes.TrimRight(chunk, "\n"), '\n'); i >= 0 {
			chunk, line = chunk[:i], chunk[i+1:]
		} else {
			chunk, line = nil, chunk
		}
		// Most lines are neither a reply nor a compaction: skip them without decoding.
		if !bytes.Contains(line, []byte(`"usage"`)) && !bytes.Contains(line, []byte(`compact_boundary`)) {
			continue
		}
		var parsed transcriptLine
		if json.Unmarshal(line, &parsed) != nil { // a line still being written
			continue
		}
		if parsed.Type == "system" && parsed.Subtype == "compact_boundary" {
			return Context{}, false, true
		}
		if parsed.Type != "assistant" || parsed.Message == nil || parsed.Message.Usage == nil || parsed.Message.Model == syntheticModel {
			continue
		}
		usage := parsed.Message.Usage
		return Context{Tokens: usage.Input + usage.CacheRead + usage.CacheCreation, Model: modelName(parsed.Message.Model), At: parsed.Timestamp}, true, true
	}
	return Context{}, false, false
}

// modelName keeps a model name to the characters model IDs use, at most 60: it is the one piece
// of transcript text that reaches the bus.
func modelName(model string) string {
	model = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_:[]@/", r) {
			return r
		}
		return -1
	}, model)
	return model[:min(len(model), 60)]
}

// plainAgentID is a subagent ID that is safe as part of a file name.
var plainAgentID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// WorkerTranscript is where Claude Code keeps the transcript of a subagent of a session:
// <session>/subagents/agent-<id>.jsonl beside the session's <session>.jsonl. It says false for a
// session transcript or an ID that does not fit that layout.
func WorkerTranscript(session, agentID string) (string, bool) {
	if !validTranscript(session) || !plainAgentID.MatchString(agentID) {
		return "", false
	}
	return filepath.Join(strings.TrimSuffix(session, ".jsonl"), "subagents", "agent-"+agentID+".jsonl"), true
}

// validTranscript is a path a hook may name as its transcript: absolute and a .jsonl file.
func validTranscript(path string) bool {
	return filepath.IsAbs(path) && strings.HasSuffix(path, ".jsonl")
}

// contextCache reads a transcript again only when its size or time changed.
type contextCache struct {
	read    func(string) (Context, bool)
	entries map[string]cachedContext
}

type cachedContext struct {
	size    int64
	modTime time.Time
	context Context
	ok      bool
}

func newContextCache() *contextCache {
	return &contextCache{read: ReadContext, entries: map[string]cachedContext{}}
}

// of is the context of the transcript at path.
func (cache *contextCache) of(path string) (Context, bool) {
	info, err := os.Stat(path)
	if err != nil {
		delete(cache.entries, path)
		return Context{}, false
	}
	if entry, ok := cache.entries[path]; ok && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
		return entry.context, entry.ok
	}
	context, ok := cache.read(path)
	cache.entries[path] = cachedContext{size: info.Size(), modTime: info.ModTime(), context: context, ok: ok}
	return context, ok
}

// keepOnly forgets every path but these, so the cache holds no more than the session and its
// running workers.
func (cache *contextCache) keepOnly(paths map[string]bool) {
	for path := range cache.entries {
		if !paths[path] {
			delete(cache.entries, path)
		}
	}
}

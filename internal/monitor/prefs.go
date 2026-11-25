package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const (
	maxPrefsBytes     = 256 << 10
	maxPrefChannels   = 2000 // read markers
	maxPrefFavourites = 200
	maxPrefSections   = 20
	maxPrefNameLength = 200
)

// Prefs are the owner's settings that every client shares: there is one password and so one
// user. Theme, layout and filters stay in each client.
type Prefs struct {
	Read       map[string]uint64 `json:"read"`       // channel ID -> last read stream sequence
	Favourites []string          `json:"favourites"` // channel IDs
	Collapsed  []string          `json:"collapsed"`  // folded sidebar sections: favourites, channels, dms, agents
}

func (p Prefs) clone() Prefs {
	return Prefs{Read: maps.Clone(p.Read), Favourites: slices.Clone(p.Favourites), Collapsed: slices.Clone(p.Collapsed)}
}

var errPrefsSave = errors.New("saving the prefs")

// prefsStore keeps the prefs in memory and in a file, and tells subscribers of every change.
type prefsStore struct {
	path   string // empty: kept in memory only
	hub    hub[Prefs]
	report func(error)

	mu    sync.Mutex
	prefs Prefs
}

// loadPrefs reads the file. A missing file means empty prefs; a corrupt one is reported and the
// monitor starts empty (the next change replaces it).
func loadPrefs(path string, report func(error)) *prefsStore {
	store := &prefsStore{path: path, report: report, prefs: Prefs{Read: map[string]uint64{}, Favourites: []string{}, Collapsed: []string{}}}
	if path == "" {
		return store
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store
	}
	if err == nil {
		var loaded Prefs
		if err = json.Unmarshal(data, &loaded); err == nil {
			if loaded.Read != nil {
				store.prefs.Read = loaded.Read
			}
			if loaded.Favourites != nil {
				store.prefs.Favourites = loaded.Favourites
			}
			if loaded.Collapsed != nil {
				store.prefs.Collapsed = loaded.Collapsed
			}
			return store
		}
	}
	report(fmt.Errorf("reading the prefs %s, starting empty: %w", path, err))
	return store
}

func (store *prefsStore) get() Prefs {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.prefs.clone()
}

// change applies edit to a copy, saves it and publishes it; on an error nothing changes.
func (store *prefsStore) change(edit func(*Prefs) error) (Prefs, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	next := store.prefs.clone()
	if err := edit(&next); err != nil {
		return Prefs{}, err
	}
	if err := store.save(next); err != nil {
		return Prefs{}, fmt.Errorf("%w: %w", errPrefsSave, err)
	}
	store.prefs = next
	store.hub.publish(next.clone())
	return next.clone(), nil
}

// save writes a temp file and renames it over the old one, so a crash never leaves half a file.
func (store *prefsStore) save(prefs Prefs) error {
	if store.path == "" {
		return nil
	}
	data, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	temp := store.path + ".tmp"
	if err := files.Write(temp, data, files.SecretMode, true); err != nil {
		return err
	}
	return os.Rename(temp, store.path)
}

// mergeRead raises the markers to the given sequences; a lower value never moves one back.
func (store *prefsStore) mergeRead(updates map[string]uint64) (map[string]uint64, error) {
	prefs, err := store.change(func(prefs *Prefs) error {
		for channel, sequence := range updates {
			prefs.Read[channel] = max(prefs.Read[channel], sequence)
		}
		if len(prefs.Read) > maxPrefChannels {
			return fmt.Errorf("more than %d channels have a read marker", maxPrefChannels)
		}
		return nil
	})
	return prefs.Read, err
}

func (store *prefsStore) setFavourites(channels []string) error {
	_, err := store.change(func(prefs *Prefs) error { prefs.Favourites = channels; return nil })
	return err
}

func (store *prefsStore) setCollapsed(sections []string) error {
	_, err := store.change(func(prefs *Prefs) error { prefs.Collapsed = sections; return nil })
	return err
}

// Prefs returns the shared preferences.
func (monitor *Monitor) Prefs() Prefs { return monitor.prefs.get() }

// SubscribePrefs returns the full prefs after every change, until unsubscribe is called or the
// subscriber falls behind and the channel is closed.
func (monitor *Monitor) SubscribePrefs() (<-chan Prefs, func()) { return monitor.prefs.hub.subscribe() }

// decodePrefsBody reads a JSON body of at most maxPrefsBytes into into and answers 400 itself
// when it cannot.
func decodePrefsBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "prefs want Content-Type: application/json")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPrefsBytes))
	if err := decoder.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	return true
}

func validName(name string) bool { return name != "" && len(name) <= maxPrefNameLength }

func (monitor *Monitor) servePrefs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, monitor.Prefs())
}

// servePrefsRead takes {"<channel>": <sequence>} and answers the merged read map. The values are
// parsed from their JSON text, so a negative, fractional or quoted one is refused.
func (monitor *Monitor) servePrefsRead(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !decodePrefsBody(w, r, &raw) {
		return
	}
	if raw == nil || len(raw) > maxPrefChannels { // nil: the body was null
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("at most %d channels", maxPrefChannels))
		return
	}
	updates := make(map[string]uint64, len(raw))
	for channel, number := range raw {
		sequence, err := strconv.ParseUint(string(number), 10, 64)
		if !validName(channel) || err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "want {\"<channel>\": <sequence>} with non-negative integers")
			return
		}
		updates[channel] = sequence
	}
	read, err := monitor.prefs.mergeRead(updates)
	monitor.answerPrefsChange(w, err, read)
}

func (monitor *Monitor) servePrefsFavourites(w http.ResponseWriter, r *http.Request) {
	monitor.servePrefsList(w, r, maxPrefFavourites, monitor.prefs.setFavourites)
}

func (monitor *Monitor) servePrefsCollapsed(w http.ResponseWriter, r *http.Request) {
	monitor.servePrefsList(w, r, maxPrefSections, monitor.prefs.setCollapsed)
}

func (monitor *Monitor) servePrefsList(w http.ResponseWriter, r *http.Request, limit int, replace func([]string) error) {
	var list []string
	if !decodePrefsBody(w, r, &list) {
		return
	}
	if list == nil || len(list) > limit || slices.ContainsFunc(list, func(name string) bool { return !validName(name) }) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("want an array of at most %d short strings", limit))
		return
	}
	monitor.answerPrefsChange(w, replace(list), monitor.Prefs())
}

// answerPrefsChange answers a saved change with value, or a failed save with a 500.
func (monitor *Monitor) answerPrefsChange(w http.ResponseWriter, err error, value any) {
	switch {
	case err == nil:
		writeJSON(w, value)
	case errors.Is(err, errPrefsSave):
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

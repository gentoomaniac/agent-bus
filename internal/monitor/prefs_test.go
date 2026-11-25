package monitor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// prefsServer serves a monitor whose prefs live in dir, with a login. report receives the
// monitor's errors.
func prefsServer(t *testing.T, dir string, report func(error)) (*Monitor, map[string]string, func(method, path, body string) (int, string)) {
	t.Helper()
	monitor := New(Config{Admin: newFakeBus(t), History: newFakeBus(t), Report: report, PrefsPath: filepath.Join(dir, "prefs.json")})
	server := serve(monitor)
	t.Cleanup(server.Close)
	headers := map[string]string{"Content-Type": "application/json"}
	send := func(method, path, body string) (int, string) {
		response, text := call(t, server, method, path, body, headers)
		return response.StatusCode, text
	}
	return monitor, headers, send
}

func failTest(t *testing.T) func(error) { return func(err error) { t.Error(err) } }

func TestPrefsStartEmpty(t *testing.T) {
	_, _, send := prefsServer(t, t.TempDir(), failTest(t))
	_, body := send("GET", "/api/v1/prefs", "")
	if strings.TrimSpace(body) != `{"read":{},"favourites":[],"subscribed":[],"collapsed":[]}` {
		t.Errorf("empty prefs = %s", body)
	}
}

func TestReadMarkersMergeByMax(t *testing.T) {
	_, _, send := prefsServer(t, t.TempDir(), failTest(t))
	send("POST", "/api/v1/prefs/read", `{"general":10,"group:a":5}`)
	status, body := send("POST", "/api/v1/prefs/read", `{"general":4,"group:a":7,"group:b":1}`)
	var read map[string]uint64
	json.Unmarshal([]byte(body), &read)
	if status != 200 || read["general"] != 10 || read["group:a"] != 7 || read["group:b"] != 1 {
		t.Errorf("merged read = %d %v", status, read)
	}
}

func TestFavouritesAndCollapsedAreReplaced(t *testing.T) {
	monitor, _, send := prefsServer(t, t.TempDir(), failTest(t))
	send("PUT", "/api/v1/prefs/favourites", `["general","group:a"]`)
	send("PUT", "/api/v1/prefs/favourites", `["group:b"]`)
	send("PUT", "/api/v1/prefs/collapsed", `["dms"]`)
	prefs := monitor.Prefs()
	if len(prefs.Favourites) != 1 || prefs.Favourites[0] != "group:b" || len(prefs.Collapsed) != 1 || prefs.Collapsed[0] != "dms" {
		t.Errorf("prefs = %+v", prefs)
	}
}

func TestSubscribedIsReplaced(t *testing.T) {
	monitor, _, send := prefsServer(t, t.TempDir(), failTest(t))
	send("PUT", "/api/v1/prefs/subscribed", `["general","group:a"]`)
	send("PUT", "/api/v1/prefs/subscribed", `["dm:x"]`)
	prefs := monitor.Prefs()
	if len(prefs.Subscribed) != 1 || prefs.Subscribed[0] != "dm:x" || len(prefs.Favourites) != 0 {
		t.Errorf("prefs = %+v", prefs)
	}
}

func TestPrefsAreValidated(t *testing.T) {
	_, headers, send := prefsServer(t, t.TempDir(), failTest(t))
	tooMany := `["` + strings.Repeat(`x","`, maxPrefFavourites) + `x"]`
	for name, test := range map[string]struct{ method, path, body string }{
		"negative sequence":   {"POST", "read", `{"general":-1}`},
		"fractional sequence": {"POST", "read", `{"general":1.5}`},
		"string sequence":     {"POST", "read", `{"general":"3"}`},
		"empty channel":       {"POST", "read", `{"":3}`},
		"long channel":        {"POST", "read", `{"` + strings.Repeat("c", maxPrefNameLength+1) + `":3}`},
		"null read":           {"POST", "read", `null`},
		"read as array":       {"POST", "read", `["general"]`},
		"object as list":      {"PUT", "favourites", `{"general":1}`},
		"null list":           {"PUT", "favourites", `null`},
		"number in list":      {"PUT", "collapsed", `[1]`},
		"empty name":          {"PUT", "collapsed", `[""]`},
		"too many favourites": {"PUT", "favourites", tooMany},
		"null subscribed":     {"PUT", "subscribed", `null`},
		"empty subscribed":    {"PUT", "subscribed", `[""]`},
		"too many subscribed": {"PUT", "subscribed", `["` + strings.Repeat(`x","`, maxPrefSubscribed) + `x"]`},
		"too many sections":   {"PUT", "collapsed", `["` + strings.Repeat(`x","`, maxPrefSections) + `x"]`},
		"oversized body":      {"PUT", "favourites", `["` + strings.Repeat("x", maxPrefsBytes) + `"]`},
	} {
		status, body := send(test.method, "/api/v1/prefs/"+test.path, test.body)
		if status != http.StatusBadRequest || errorCode(body) != "bad_request" {
			t.Errorf("%s: %d %s, want 400 bad_request", name, status, body)
		}
	}
	headers["Content-Type"] = "text/plain"
	if status, _ := send("POST", "/api/v1/prefs/read", `{}`); status != http.StatusUnsupportedMediaType {
		t.Errorf("wrong content type: %d", status)
	}
}

func TestReadMarkerCountIsBounded(t *testing.T) {
	_, _, send := prefsServer(t, t.TempDir(), failTest(t))
	body := func(from, to int) string {
		entries := make([]string, 0, to-from)
		for i := from; i < to; i++ {
			entries = append(entries, fmt.Sprintf(`"c%d":1`, i))
		}
		return "{" + strings.Join(entries, ",") + "}"
	}
	if status, _ := send("POST", "/api/v1/prefs/read", body(0, maxPrefChannels)); status != 200 {
		t.Errorf("the allowed number of markers: %d", status)
	}
	if status, _ := send("POST", "/api/v1/prefs/read", body(maxPrefChannels, maxPrefChannels+1)); status != http.StatusBadRequest {
		t.Errorf("one marker more than allowed: %d", status)
	}
}

func TestPrefsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	_, _, send := prefsServer(t, dir, failTest(t))
	send("POST", "/api/v1/prefs/read", `{"general":9}`)
	send("PUT", "/api/v1/prefs/favourites", `["general"]`)
	send("PUT", "/api/v1/prefs/subscribed", `["group:a"]`)
	send("PUT", "/api/v1/prefs/collapsed", `["agents"]`)

	again, _, _ := prefsServer(t, dir, failTest(t))
	prefs := again.Prefs()
	if prefs.Read["general"] != 9 || len(prefs.Favourites) != 1 || len(prefs.Subscribed) != 1 || len(prefs.Collapsed) != 1 {
		t.Errorf("after a restart: %+v", prefs)
	}
	info, err := os.Stat(filepath.Join(dir, "prefs.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("prefs file: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prefs.json.tmp")); err == nil {
		t.Error("the temp file was left behind")
	}
}

func TestACorruptPrefsFileIsReportedAndStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prefs.json"), []byte("{not json"), 0o600)
	var reported []error
	monitor, _, send := prefsServer(t, dir, func(err error) { reported = append(reported, err) })
	if len(reported) != 1 {
		t.Errorf("reported %v", reported)
	}
	if prefs := monitor.Prefs(); len(prefs.Read) != 0 || len(prefs.Favourites) != 0 {
		t.Errorf("prefs = %+v", prefs)
	}
	if status, _ := send("PUT", "/api/v1/prefs/favourites", `["general"]`); status != 200 {
		t.Errorf("a change after a corrupt file: %d", status)
	}
}

func TestPrefsNeedTheLoginAndTheOrigin(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/api/v1/prefs"}, {"POST", "/api/v1/prefs/read"}, {"PUT", "/api/v1/prefs/favourites"}, {"PUT", "/api/v1/prefs/subscribed"}, {"PUT", "/api/v1/prefs/collapsed"},
	} {
		_, body := call(t, server, endpoint.method, endpoint.path, `{}`, map[string]string{"Content-Type": "application/json"})
		if errorCode(body) != "unauthenticated" {
			t.Errorf("%s %s without a login: %q", endpoint.method, endpoint.path, errorCode(body))
		}
	}
	_, token := login(t, server, testPassword, nil)
	cookie := map[string]string{"Cookie": sessionCookie + "=" + token, "Content-Type": "application/json"}
	if _, body := call(t, server, "PUT", "/api/v1/prefs/collapsed", `["dms"]`, cookie); errorCode(body) != "forbidden_origin" {
		t.Errorf("a cookie without an origin: %q", errorCode(body))
	}
	cookie["Origin"] = server.URL
	if response, _ := call(t, server, "PUT", "/api/v1/prefs/collapsed", `["dms"]`, cookie); response.StatusCode != 200 {
		t.Errorf("a cookie with the own origin: %d", response.StatusCode)
	}
	if response, _ := call(t, server, "GET", "/api/v1/prefs", "", cookie); response.StatusCode != 200 {
		t.Errorf("reading with the cookie: %d", response.StatusCode)
	}
}

func TestEventStreamDeliversPrefs(t *testing.T) {
	monitor, _, _ := prefsServer(t, t.TempDir(), failTest(t))
	server := serve(monitor)
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := bufio.NewScanner(response.Body)
	lines.Scan() // ": connected": the subscription exists now

	if _, err := monitor.prefs.mergeRead(map[string]uint64{"general": 3}); err != nil {
		t.Fatal(err)
	}
	found := make(chan Prefs, 1)
	go func() {
		isPrefs := false
		for lines.Scan() {
			if lines.Text() == "event: prefs" {
				isPrefs = true
			}
			if data, isData := strings.CutPrefix(lines.Text(), "data: "); isData && isPrefs {
				var prefs Prefs
				json.Unmarshal([]byte(data), &prefs)
				found <- prefs
				return
			}
		}
	}()
	select {
	case prefs := <-found:
		if prefs.Read["general"] != 3 || prefs.Favourites == nil || prefs.Subscribed == nil {
			t.Errorf("event = %+v", prefs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no prefs event arrived")
	}
}

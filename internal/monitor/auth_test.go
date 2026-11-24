package monitor

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const testPassword = "correct horse battery"

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) advance(d time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(d)
}

// serveWithPassword serves a monitor with the password testPassword and a clock the test controls.
func serveWithPassword(t *testing.T, access Access) (*httptest.Server, *fakeClock) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	access.PasswordHash = hash
	clock := &fakeClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	server := httptest.NewUnstartedServer(nil)
	if access.Listen == "" {
		access.Listen = server.Listener.Addr().String()
	}
	handler, err := runMonitor(t, newFakeBus(t)).handler(access, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	return server, clock
}

// call sends a request and returns the response with its body read and closed.
func call(t *testing.T, server *httptest.Server, method, path, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		if name == "Host" {
			request.Host = value
		} else {
			request.Header.Set(name, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var text strings.Builder
	buffer := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buffer)
		text.Write(buffer[:n])
		if err != nil {
			break
		}
	}
	return response, text.String()
}

func errorCode(body string) string {
	var parsed struct{ Code string }
	json.Unmarshal([]byte(body), &parsed)
	return parsed.Code
}

// login returns the response and the session token from its body.
func login(t *testing.T, server *httptest.Server, password string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	headers = mergeHeaders(map[string]string{"Content-Type": "application/json"}, headers)
	body, _ := json.Marshal(map[string]string{"password": password})
	response, text := call(t, server, "POST", "/api/v1/login", string(body), headers)
	var parsed struct{ Token string }
	json.Unmarshal([]byte(text), &parsed)
	return response, parsed.Token
}

func mergeHeaders(base, extra map[string]string) map[string]string {
	merged := map[string]string{}
	for _, headers := range []map[string]string{base, extra} {
		for name, value := range headers {
			merged[name] = value
		}
	}
	return merged
}

func statusWith(t *testing.T, server *httptest.Server, headers map[string]string) (int, string) {
	t.Helper()
	response, body := call(t, server, "GET", "/api/v1/status", "", headers)
	return response.StatusCode, errorCode(body)
}

func TestEverythingButTheLoginNeedsThePassword(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	for _, path := range []string{"/api/v1/status", "/api/v1/channels", "/api/v1/agents", "/api/v1/presence", "/api/v1/members?channel=general", "/api/v1/messages", "/api/v1/events"} {
		response, body := call(t, server, "GET", path, "", nil)
		if response.StatusCode != http.StatusUnauthorized || errorCode(body) != "unauthenticated" {
			t.Errorf("GET %s without login: %d %s", path, response.StatusCode, body)
		}
	}
	if response, _ := call(t, server, "GET", "/", "", nil); response.StatusCode != http.StatusOK {
		t.Errorf("the page needs no login: %d", response.StatusCode)
	}
}

func TestLoginSetsAHardenedCookieAndReturnsAToken(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	response, token := login(t, server, testPassword, nil)
	if response.StatusCode != http.StatusOK || token == "" {
		t.Fatalf("login: %d, token %q", response.StatusCode, token)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Value != token || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Secure || cookies[0].Path != "/" {
		t.Fatalf("cookie = %+v", cookies)
	}
	if status, _ := statusWith(t, server, map[string]string{"Cookie": cookies[0].String()}); status != http.StatusOK {
		t.Errorf("status with the cookie: %d", status)
	}
	if status, _ := statusWith(t, server, map[string]string{"Authorization": "Bearer " + token}); status != http.StatusOK {
		t.Errorf("status with the bearer token: %d", status)
	}
	if status, _ := statusWith(t, server, map[string]string{"Authorization": "Bearer nonsense"}); status != http.StatusUnauthorized {
		t.Errorf("status with a wrong token: %d", status)
	}
	if response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("the API sends CORS headers")
	}
}

func TestWrongPasswordIsRefused(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	response, _ := login(t, server, "wrong", nil)
	if response.StatusCode != http.StatusUnauthorized || len(response.Cookies()) != 0 {
		t.Errorf("wrong password: %d, cookies %v", response.StatusCode, response.Cookies())
	}
	body, _ := json.Marshal(map[string]string{"password": testPassword})
	if response, text := call(t, server, "POST", "/api/v1/login", string(body), nil); response.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("login without a JSON content type: %d %s", response.StatusCode, text)
	}
}

func TestBasicAuthWithThePassword(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	request, _ := http.NewRequest("GET", server.URL+"/api/v1/status", nil)
	request.SetBasicAuth("anyone", testPassword)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("Basic with the password: %d", response.StatusCode)
	}
	request.SetBasicAuth("anyone", "wrong")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("Basic with a wrong password: %d", response.StatusCode)
	}
}

func TestLoginRotatesTheSession(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	_, first := login(t, server, testPassword, nil)
	_, second := login(t, server, testPassword, map[string]string{"Cookie": sessionCookie + "=" + first})
	if second == "" || second == first {
		t.Fatalf("tokens %q then %q", first, second)
	}
	if status, _ := statusWith(t, server, map[string]string{"Authorization": "Bearer " + first}); status != http.StatusUnauthorized {
		t.Errorf("the old session still works after a login with it: %d", status)
	}
	if status, _ := statusWith(t, server, map[string]string{"Authorization": "Bearer " + second}); status != http.StatusOK {
		t.Errorf("the new session: %d", status)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	_, token := login(t, server, testPassword, nil)
	cookie := map[string]string{"Cookie": sessionCookie + "=" + token, "Origin": server.URL}
	response, _ := call(t, server, "POST", "/api/v1/logout", "", cookie)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", response.StatusCode)
	}
	if cleared := response.Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Errorf("the cookie is not cleared: %+v", cleared)
	}
	if status, _ := statusWith(t, server, map[string]string{"Authorization": "Bearer " + token}); status != http.StatusUnauthorized {
		t.Errorf("the session works after logout: %d", status)
	}
}

func TestSessionsExpire(t *testing.T) {
	server, clock := serveWithPassword(t, Access{})
	bearer := func(token string) map[string]string { return map[string]string{"Authorization": "Bearer " + token} }

	_, idle := login(t, server, testPassword, nil)
	clock.advance(sessionIdle - time.Minute)
	if status, _ := statusWith(t, server, bearer(idle)); status != http.StatusOK {
		t.Fatalf("before the idle limit: %d", status)
	}
	clock.advance(sessionIdle - time.Minute) // the use above counted as activity
	if status, _ := statusWith(t, server, bearer(idle)); status != http.StatusOK {
		t.Fatalf("activity did not extend the session: %d", status)
	}
	clock.advance(sessionIdle + time.Minute)
	if status, _ := statusWith(t, server, bearer(idle)); status != http.StatusUnauthorized {
		t.Errorf("after the idle limit: %d", status)
	}

	_, busy := login(t, server, testPassword, nil)
	for elapsed := time.Duration(0); elapsed < sessionMax; elapsed += time.Hour {
		clock.advance(time.Hour)
		if elapsed+time.Hour < sessionMax {
			statusWith(t, server, bearer(busy))
		}
	}
	clock.advance(time.Minute)
	if status, _ := statusWith(t, server, bearer(busy)); status != http.StatusUnauthorized {
		t.Errorf("after the maximum lifetime: %d", status)
	}
}

func TestLoginIsRateLimitedPerClient(t *testing.T) {
	server, clock := serveWithPassword(t, Access{})
	for range freeLoginFailures {
		if response, _ := login(t, server, "wrong", nil); response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a free failure: %d", response.StatusCode)
		}
	}
	// Now even the right password waits.
	request, _ := json.Marshal(map[string]string{"password": testPassword})
	response, body := call(t, server, "POST", "/api/v1/login", string(request), map[string]string{"Content-Type": "application/json"})
	if response.StatusCode != http.StatusTooManyRequests || errorCode(body) != "too_many_attempts" || response.Header.Get("Retry-After") == "" {
		t.Fatalf("after the free failures: %d %s", response.StatusCode, body)
	}
	clock.advance(2 * time.Second)
	if response, _ := login(t, server, "wrong", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after the first delay: %d", response.StatusCode)
	}
	clock.advance(time.Second) // the delay doubled to 2 s
	if response, _ := login(t, server, testPassword, nil); response.StatusCode != http.StatusTooManyRequests {
		t.Errorf("the delay did not grow: %d", response.StatusCode)
	}
	clock.advance(3 * time.Second)
	if response, _ := login(t, server, testPassword, nil); response.StatusCode != http.StatusOK {
		t.Errorf("the right password after waiting: %d", response.StatusCode)
	}
	if response, _ := login(t, server, "wrong", nil); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("a success should clear the failures: %d", response.StatusCode)
	}
}

func TestBasicAuthCountsAgainstTheLoginLimit(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	for range freeLoginFailures {
		call(t, server, "GET", "/api/v1/status", "", map[string]string{"Authorization": "Basic d3Jvbmc6d3Jvbmc="}) // wrong:wrong
	}
	if response, _ := login(t, server, testPassword, nil); response.StatusCode != http.StatusTooManyRequests {
		t.Errorf("login after Basic failures: %d", response.StatusCode)
	}
}

func basic(password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("anyone:"+password))
}

const sendBody = `{"to":{"kind":"group","name":"general"},"type":"message","text":"hi"}`

// sendCode sends a message and returns the error code. The test monitors are read-only, so
// "read_only" is what a request that passed authentication and the origin check gets.
func sendCode(t *testing.T, server *httptest.Server, headers map[string]string) string {
	t.Helper()
	_, body := call(t, server, "POST", "/api/v1/send", sendBody, mergeHeaders(map[string]string{"Content-Type": "application/json"}, headers))
	return errorCode(body)
}

func TestMutationsNeedTheSameOrigin(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	_, token := login(t, server, testPassword, nil)
	cookie := sessionCookie + "=" + token

	for name, test := range map[string]struct {
		headers map[string]string
		want    string
	}{
		"cookie, no origin":            {map[string]string{"Cookie": cookie}, "forbidden_origin"},
		"cookie, foreign origin":       {map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}, "forbidden_origin"},
		"cookie, null origin":          {map[string]string{"Cookie": cookie, "Origin": "null"}, "forbidden_origin"},
		"cookie, own origin":           {map[string]string{"Cookie": cookie, "Origin": server.URL}, "read_only"},
		"cookie, own referer":          {map[string]string{"Cookie": cookie, "Referer": server.URL + "/page"}, "read_only"},
		"cookie, foreign referer":      {map[string]string{"Cookie": cookie, "Referer": "https://evil.example/"}, "forbidden_origin"},
		"bearer, no origin":            {map[string]string{"Authorization": "Bearer " + token}, "read_only"},
		"bearer, foreign origin":       {map[string]string{"Authorization": "Bearer " + token, "Origin": "https://evil.example"}, "read_only"},
		"basic, no origin":             {map[string]string{"Authorization": basic(testPassword)}, "read_only"},
		"foreign origin and no cookie": {map[string]string{"Origin": "https://evil.example"}, "unauthenticated"},
	} {
		if got := sendCode(t, server, test.headers); got != test.want {
			t.Errorf("%s: code %q, want %q", name, got, test.want)
		}
	}
	if response, _ := call(t, server, "POST", "/api/v1/logout", "", map[string]string{"Cookie": cookie}); response.StatusCode != http.StatusForbidden {
		t.Errorf("logout without an origin: %d", response.StatusCode)
	}
}

func TestLoginRefusesAForeignOrigin(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	if response, _ := login(t, server, testPassword, map[string]string{"Origin": "https://evil.example"}); response.StatusCode != http.StatusForbidden {
		t.Errorf("login from a foreign page: %d", response.StatusCode)
	}
	if response, _ := login(t, server, testPassword, map[string]string{"Origin": server.URL}); response.StatusCode != http.StatusOK {
		t.Errorf("login from the page: %d", response.StatusCode)
	}
}

func TestOpenMonitorWithoutAPassword(t *testing.T) {
	server := serve(runMonitor(t, newFakeBus(t)))
	defer server.Close()
	if got := sendCode(t, server, nil); got != "read_only" {
		t.Errorf("a curl-like send: %q", got)
	}
	if got := sendCode(t, server, map[string]string{"Origin": "https://evil.example"}); got != "forbidden_origin" {
		t.Errorf("a send from a foreign page: %q", got)
	}
	if response, body := call(t, server, "POST", "/api/v1/login", `{"password":"x"}`, map[string]string{"Content-Type": "application/json"}); response.StatusCode != http.StatusConflict || errorCode(body) != "no_password_set" {
		t.Errorf("login without a password: %d %s", response.StatusCode, body)
	}
	var status struct {
		PasswordSet bool `json:"password_set"`
	}
	getJSON(t, server, "/api/v1/status", &status)
	if status.PasswordSet {
		t.Error("status says a password is set")
	}
}

func TestBehindAnHTTPSProxy(t *testing.T) {
	server, _ := serveWithPassword(t, Access{Listen: "0.0.0.0:8080", BehindProxy: true, PublicURL: mustURL(t, "https://bus.example")})
	page := map[string]string{"Host": "bus.example", "Origin": "https://bus.example"}

	response, token := login(t, server, testPassword, page)
	if cookies := response.Cookies(); response.StatusCode != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("login behind the proxy: %d, cookies %+v", response.StatusCode, response.Cookies())
	}
	cookie := map[string]string{"Cookie": sessionCookie + "=" + token}
	if got := sendCode(t, server, mergeHeaders(cookie, page)); got != "read_only" {
		t.Errorf("send from the public origin: %q", got)
	}
	// The address clients used inside the network is not the page's origin.
	if got := sendCode(t, server, mergeHeaders(cookie, map[string]string{"Host": "bus.example", "Origin": server.URL})); got != "forbidden_origin" {
		t.Errorf("send from another origin: %q", got)
	}
	if response, _ := login(t, server, testPassword, map[string]string{"Host": "other.example"}); response.StatusCode != http.StatusForbidden {
		t.Errorf("a Host that is not the public one: %d", response.StatusCode)
	}
}

func TestEventsEndWhenTheSessionEnds(t *testing.T) {
	defer func(previous time.Duration) { eventKeepAlive = previous }(eventKeepAlive)
	eventKeepAlive = 10 * time.Millisecond
	server, clock := serveWithPassword(t, Access{})
	_, token := login(t, server, testPassword, nil)
	request, _ := http.NewRequest("GET", server.URL+"/api/v1/events", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", response.StatusCode)
	}
	clock.advance(sessionMax + time.Hour)

	ended := make(chan struct{})
	go func() { io.Copy(io.Discard, response.Body); close(ended) }()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream outlived its session")
	}
}

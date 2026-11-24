package monitor

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
	"golang.org/x/crypto/bcrypt"
)

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	server, _ := serveWithPassword(t, Access{})
	_, token := login(t, server, testPassword, nil)
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		if status, code := statusWith(t, server, map[string]string{"Authorization": scheme + " " + token}); status != http.StatusOK {
			t.Errorf("scheme %q: %d %s", scheme, status, code)
		}
	}
	if status, _ := statusWith(t, server, map[string]string{"Authorization": "Bearerx " + token}); status != http.StatusUnauthorized {
		t.Errorf("a scheme that only starts with Bearer: %d", status)
	}
}

func TestConcurrentGuessesCannotBypassTheLoginLimit(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	login := newAuth(hash, func() time.Time { return now })

	const guesses = 100
	var wrong, refused int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range guesses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := login.attempt("203.0.113.9", "wrong")
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, errBadCredentials) {
				wrong++
			} else {
				refused++
			}
		}()
	}
	wg.Wait()
	if wrong != freeLoginFailures || refused != guesses-freeLoginFailures {
		t.Errorf("%d guesses were compared and %d refused, want %d and %d", wrong, refused, freeLoginFailures, guesses-freeLoginFailures)
	}
}

func TestEventStreamWritesNothingAfterItsSessionEnded(t *testing.T) {
	defer func(previous time.Duration) { eventKeepAlive = previous }(eventKeepAlive)
	eventKeepAlive = time.Hour // only the writes of events can notice the ended session
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	web := runMonitor(t, newFakeBus(t))
	server := httptest.NewUnstartedServer(nil)
	handler, err := web.handler(Access{Listen: server.Listener.Addr().String(), PasswordHash: hash}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)

	_, token := login(t, server, testPassword, nil)
	request, _ := http.NewRequest("GET", server.URL+"/api/v1/events", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	clock.advance(sessionMax + time.Hour)

	record, _ := json.Marshal(presence.Record{State: presence.Working, Updated: time.Now()})
	web.receivePresence("api-client@alice", record)
	body := make(chan string, 1)
	go func() { text, _ := io.ReadAll(response.Body); body <- string(text) }()
	select {
	case text := <-body:
		if strings.Contains(text, "event: presence") {
			t.Errorf("an event went out after the session ended: %s", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream outlived its session")
	}
}

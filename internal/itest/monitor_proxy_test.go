//go:build integration

package itest

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
	"golang.org/x/crypto/bcrypt"
)

// TestMonitorBehindAProxy runs the monitor as docs/monitor.md describes a deployment: bound to a
// non-loopback address in the proxy's network (--behind-proxy), reached through a reverse proxy that
// presents the public host and https. It checks login, an API call, the event stream and a send.
func TestMonitorBehindAProxy(t *testing.T) {
	const publicOrigin = "https://bus.example"
	const password = "a password for the test"
	h := startBus(t)
	h.register(t, monitorAgent)

	sender, _ := h.startAdapter(t, monitorAgent)
	monitorAdmin, monitorHistory := h.monitorReads(t)
	web := monitor.New(monitor.Config{
		Admin: monitorAdmin, History: monitorHistory, Sender: sender,
		Report: func(err error) { t.Errorf("monitor: %v", err) },
	})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { web.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewUnstartedServer(nil)
	handler, err := web.Handler(monitor.Access{
		Listen: "0.0.0.0:8080", BehindProxy: true, PasswordHash: hash, PublicURL: mustParseURL(t, publicOrigin),
	})
	if err != nil {
		t.Fatal(err)
	}
	backend.Config.Handler = handler
	backend.Start()
	t.Cleanup(backend.Close)

	target, _ := url.Parse(backend.URL)
	proxy := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded()
			r.Out.Host = "bus.example"
			r.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		FlushInterval: -1, // the event stream must not be buffered
	})
	t.Cleanup(proxy.Close)

	call := func(method, path, body string, headers map[string]string) (*http.Response, error) {
		request, err := http.NewRequest(method, proxy.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		return http.DefaultClient.Do(request)
	}

	response, err := call("GET", "/api/v1/status", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without login: %d, want 401", response.StatusCode)
	}

	loginBody, _ := json.Marshal(map[string]string{"password": password})
	response, err = call("POST", "/api/v1/login", string(loginBody), map[string]string{"Content-Type": "application/json", "Origin": publicOrigin})
	if err != nil {
		t.Fatal(err)
	}
	var login struct{ Token string }
	json.NewDecoder(response.Body).Decode(&login)
	response.Body.Close()
	if cookies := response.Cookies(); response.StatusCode != http.StatusOK || login.Token == "" || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("login: %d, token %q, cookies %+v", response.StatusCode, login.Token, cookies)
	}
	session := map[string]string{"Cookie": "bus_session=" + login.Token, "Origin": publicOrigin}

	response, err = call("GET", "/api/v1/status", "", session)
	if err != nil {
		t.Fatal(err)
	}
	var status monitor.Status
	json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !status.CanSend || status.Agent != monitorAgent {
		t.Fatalf("status with the session: %d %+v", response.StatusCode, status)
	}

	events, err := call("GET", "/api/v1/events", "", map[string]string{"Authorization": "Bearer " + login.Token})
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	lines := bufio.NewScanner(events.Body)
	lines.Scan() // ": connected": subscribed, and the proxy passes the stream through

	send := `{"to":{"kind":"group","name":"general"},"type":"message","targets":["@all"],"text":"sent through the proxy"}`
	sendHeaders := map[string]string{"Content-Type": "application/json", "Cookie": session["Cookie"], "Origin": publicOrigin}
	response, err = call("POST", "/api/v1/send", send, sendHeaders)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("send: %d", response.StatusCode)
	}
	sendHeaders["Origin"] = "https://evil.example"
	if response, err = call("POST", "/api/v1/send", send, sendHeaders); err != nil {
		t.Fatal(err)
	} else if response.Body.Close(); response.StatusCode != http.StatusForbidden {
		t.Errorf("send from a foreign origin: %d, want 403", response.StatusCode)
	}

	arrived := make(chan monitor.Message, 1)
	go func() {
		for lines.Scan() {
			if data, isData := strings.CutPrefix(lines.Text(), "data: "); isData {
				var message monitor.Message
				if json.Unmarshal([]byte(data), &message) == nil && message.Text != "" {
					arrived <- message
					return
				}
			}
		}
	}()
	select {
	case message := <-arrived:
		if message.Text != "sent through the proxy" || message.Status != monitor.Verified {
			t.Errorf("event = %+v", message)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the sent message did not arrive on the event stream")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

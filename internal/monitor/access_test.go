package monitor

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestValidateRefusesWhatWouldExposeTheMonitor(t *testing.T) {
	for name, test := range map[string]struct {
		access  Access
		refused bool
	}{
		"loopback":                   {Access{Listen: "127.0.0.1:8080"}, false},
		"ipv6 loopback":              {Access{Listen: "[::1]:8080"}, false},
		"localhost":                  {Access{Listen: "localhost:8080"}, false},
		"all interfaces":             {Access{Listen: "0.0.0.0:8080"}, true},
		"no host":                    {Access{Listen: ":8080"}, true},
		"a LAN address":              {Access{Listen: "192.168.1.5:8080"}, true},
		"all interfaces, proxied":    {Access{Listen: "0.0.0.0:8080", BehindProxy: true, PasswordHash: []byte("x")}, false},
		"proxied without password":   {Access{Listen: "0.0.0.0:8080", BehindProxy: true}, true},
		"public URL without proxy":   {Access{Listen: "127.0.0.1:8080", PublicURL: &url.URL{Scheme: "https", Host: "bus.example"}}, true},
		"trusted proxy without flag": {Access{Listen: "127.0.0.1:8080", TrustedProxies: loopbackProxies}, true},
		"public URL with a path":     {Access{Listen: "0.0.0.0:8080", BehindProxy: true, PasswordHash: []byte("x"), PublicURL: &url.URL{Scheme: "https", Host: "bus.example", Path: "/bus"}}, true},
		"public URL without scheme":  {Access{Listen: "0.0.0.0:8080", BehindProxy: true, PasswordHash: []byte("x"), PublicURL: &url.URL{Host: "bus.example"}}, true},
	} {
		if err := test.access.Validate(); (err != nil) != test.refused {
			t.Errorf("%s: err = %v, want refused %v", name, err, test.refused)
		}
	}
}

func TestAcceptsHost(t *testing.T) {
	access := Access{Listen: "0.0.0.0:8080", BehindProxy: true, PublicURL: mustURL(t, "https://bus.example"), AllowedHosts: []string{"alias.example:9000", "any.example"}}
	for host, want := range map[string]bool{
		"localhost:18080":    true, // any port: a container's published port
		"127.0.0.1":          true,
		"[::1]:8080":         true,
		"bus.example":        true,
		"BUS.example:443":    true,
		"alias.example:9000": true,
		"alias.example:9001": false, // the entry names a port
		"any.example:1234":   true,
		"rebound.example:80": false,
		"10.0.0.7:8080":      false, // the listen address 0.0.0.0 says nothing about the name
	} {
		if got := access.acceptsHost(host); got != want {
			t.Errorf("acceptsHost(%q) = %v, want %v", host, got, want)
		}
	}
	specific := Access{Listen: "192.168.1.5:8080", BehindProxy: true}
	if !specific.acceptsHost("192.168.1.5:80") || specific.acceptsHost("192.168.1.6:8080") {
		t.Error("a specific listen host is accepted with any port, others are not")
	}
}

func TestForwardedHeadersAreTrustedOnlyFromTrustedProxies(t *testing.T) {
	access := Access{Listen: "0.0.0.0:8080", BehindProxy: true, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}}
	forwarded := func(peer string) client {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = peer
		request.Host = "monitor:8080"
		request.Header.Set("X-Forwarded-Host", "bus.example")
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
		return access.clientOf(request)
	}
	trusted := forwarded("10.1.2.3:5555")
	if !trusted.secure || trusted.origin != "https://bus.example" || trusted.hostHeader != "bus.example" || trusted.address != "203.0.113.9" {
		t.Errorf("trusted proxy: %+v", trusted)
	}
	stranger := forwarded("10.2.0.1:5555")
	if stranger.secure || stranger.origin != "http://monitor:8080" || stranger.hostHeader != "monitor:8080" || stranger.address != "10.2.0.1" {
		t.Errorf("untrusted peer: %+v", stranger)
	}

	notProxied := Access{Listen: "127.0.0.1:8080"}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "127.0.0.1:5555"
	request.Header.Set("X-Forwarded-Proto", "https")
	if view := notProxied.clientOf(request); view.secure {
		t.Errorf("without --behind-proxy a forwarded header was believed: %+v", view)
	}
}

func TestDefaultTrustedProxyIsLoopback(t *testing.T) {
	access := Access{Listen: "0.0.0.0:8080", BehindProxy: true}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	request.RemoteAddr = "[::1]:1"
	if !access.clientOf(request).secure {
		t.Error("::1 is trusted by default")
	}
	request.RemoteAddr = "192.0.2.1:1"
	if access.clientOf(request).secure {
		t.Error("another peer is not trusted by default")
	}
}

func TestPublicURLDecidesOriginAndSecure(t *testing.T) {
	access := Access{Listen: "0.0.0.0:8080", BehindProxy: true, PublicURL: mustURL(t, "https://Bus.example:443")}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.1:1"
	view := access.clientOf(request)
	if !view.secure || !view.sameOrigin("https://bus.example") || view.sameOrigin("http://bus.example") || view.sameOrigin("https://evil.example") || view.sameOrigin("null") {
		t.Errorf("view %+v", view)
	}
}

func TestAllowedHostIsServed(t *testing.T) {
	server := serveWith(runMonitor(t, newFakeBus(t)), Access{AllowedHosts: []string{"bus.example"}})
	defer server.Close()
	for host, want := range map[string]int{"bus.example": http.StatusOK, "other.example": http.StatusForbidden} {
		request, _ := http.NewRequest("GET", server.URL+"/api/v1/status", nil)
		request.Host = host
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("Host %q: status %d, want %d", host, response.StatusCode, want)
		}
	}
}

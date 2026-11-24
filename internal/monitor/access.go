package monitor

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// Access says who may reach the monitor and how it learns the address a client used. The monitor
// speaks plain HTTP: on loopback it needs nothing more, anywhere else it sits behind a reverse
// proxy that does the TLS (docs/monitor.md).
type Access struct {
	Listen string // the address the listener is bound to

	// BehindProxy lets Listen be a non-loopback address, for a reverse proxy to reach. Only then do
	// TrustedProxies and PublicURL mean anything.
	BehindProxy bool
	// TrustedProxies are the peers whose X-Forwarded-* headers are believed. Default: loopback.
	TrustedProxies []netip.Prefix
	// PublicURL is the scheme and host (and port) clients use to reach the proxy. When set, it is the
	// page's origin and decides the cookie's Secure flag; forwarded headers then only tell the
	// client's address.
	PublicURL *url.URL
	// AllowedHosts are Host values accepted besides the loopback names, the listen address and the
	// public URL. An entry without a port matches any port.
	AllowedHosts []string
}

var loopbackProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}

// Validate refuses the settings that would expose the monitor by accident.
func (access Access) Validate() error {
	host, _ := splitHostPort(access.Listen)
	if !access.BehindProxy {
		if !isLoopbackHost(host) {
			return fmt.Errorf("--listen %s is not a loopback address: the monitor speaks plain HTTP, so publish it through a reverse proxy and pass --behind-proxy", access.Listen)
		}
		if len(access.TrustedProxies) > 0 || access.PublicURL != nil {
			return errors.New("--trusted-proxy and --public-url need --behind-proxy")
		}
	}
	if access.PublicURL != nil {
		if scheme := access.PublicURL.Scheme; scheme != "http" && scheme != "https" {
			return fmt.Errorf("--public-url %s: want a scheme (http or https) and a host", access.PublicURL)
		}
		if access.PublicURL.Host == "" || (access.PublicURL.Path != "" && access.PublicURL.Path != "/") {
			return fmt.Errorf("--public-url %s: want only a scheme, host and optional port", access.PublicURL)
		}
	}
	return nil
}

// ParseTrustedProxies reads addresses and CIDR ranges.
func ParseTrustedProxies(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			prefixes = append(prefixes, prefix)
		} else if address, err := netip.ParseAddr(value); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
		} else {
			return nil, fmt.Errorf("--trusted-proxy %q is neither an IP address nor a CIDR range", value)
		}
	}
	return prefixes, nil
}

// acceptsHost says whether a Host header names this monitor. Anything else is refused so that a web
// page cannot reach the monitor through a DNS name it controls that resolves here (DNS rebinding).
// The port of the loopback names and of the listen address is not compared: a container's published
// port (18080:8080) is not the one the monitor listens on.
func (access Access) acceptsHost(hostHeader string) bool {
	host, port := splitHostPort(hostHeader)
	listenHost, _ := splitHostPort(access.Listen)
	if isLoopbackHost(host) || (host == listenHost && !isUnspecifiedHost(listenHost)) {
		return true
	}
	entries := access.AllowedHosts
	if access.PublicURL != nil {
		entries = append([]string{access.PublicURL.Host}, entries...)
	}
	for _, entry := range entries {
		entryHost, entryPort := splitHostPort(entry)
		if host == entryHost && (entryPort == "" || entryPort == port) {
			return true
		}
	}
	return false
}

// client is what the monitor knows about the party that sent a request.
type client struct {
	hostHeader string // the Host the client addressed, which acceptsHost judges
	origin     string // scheme://host the page was loaded from, to compare with an Origin header
	secure     bool   // the client reached the monitor over HTTPS
	address    string // the client's IP address, for the login rate limit
}

// clientOf reads the forwarded headers only from a trusted proxy. It never trusts them from
// anybody else, and never when the monitor is not behind a proxy.
func (access Access) clientOf(r *http.Request) client {
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	view := client{hostHeader: r.Host, address: peer}
	scheme := "http"
	if access.trusts(peer) {
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			view.hostHeader, _, _ = strings.Cut(forwarded, ",")
			view.hostHeader = strings.TrimSpace(view.hostHeader)
		}
		if proto := r.Header.Get("X-Forwarded-Proto"); proto == "http" || proto == "https" {
			scheme = proto
		}
		// The last entry is the one the trusted proxy itself appended.
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			view.address = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host := view.hostHeader
	if access.PublicURL != nil {
		scheme, host = access.PublicURL.Scheme, access.PublicURL.Host
	}
	view.origin = normalizeOrigin(scheme, host)
	view.secure = scheme == "https"
	return view
}

func (access Access) trusts(peer string) bool {
	address, err := netip.ParseAddr(peer)
	if err != nil || !access.BehindProxy {
		return false
	}
	trusted := access.TrustedProxies
	if len(trusted) == 0 {
		trusted = loopbackProxies
	}
	for _, prefix := range trusted {
		if prefix.Contains(address.Unmap()) {
			return true
		}
	}
	return false
}

// sameOrigin says whether an Origin or Referer header (a URL) belongs to this page's origin.
func (view client) sameOrigin(header string) bool {
	parsed, err := url.Parse(header)
	if err != nil || parsed.Host == "" {
		return false
	}
	return normalizeOrigin(parsed.Scheme, parsed.Host) == view.origin
}

// normalizeOrigin lowercases and drops the default port, so https://Bus.example:443 equals https://bus.example.
func normalizeOrigin(scheme, host string) string {
	scheme, host = strings.ToLower(scheme), strings.ToLower(host)
	if name, port, err := net.SplitHostPort(host); err == nil && port == map[string]string{"http": "80", "https": "443"}[scheme] {
		host = name
		if strings.Contains(name, ":") {
			host = "[" + name + "]"
		}
	}
	return scheme + "://" + host
}

// splitHostPort accepts a host with or without a port, and returns the host in lower case.
func splitHostPort(hostPort string) (host, port string) {
	if host, port, err := net.SplitHostPort(hostPort); err == nil {
		return strings.ToLower(host), port
	}
	return strings.ToLower(strings.Trim(hostPort, "[]")), ""
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func isUnspecifiedHost(host string) bool {
	address, err := netip.ParseAddr(host)
	return host == "" || (err == nil && address.IsUnspecified())
}

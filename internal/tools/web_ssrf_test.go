package tools

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubResolver builds a WebTool whose DNS answers are fixed, so these tests
// assert the SSRF logic rather than whatever the CI network happens to resolve.
func stubResolver(t *testing.T, answers map[string][]string) *WebTool {
	t.Helper()
	tool := NewWebTool(30*time.Second, "")
	tool.resolveIP = func(host string) ([]net.IP, error) {
		addrs, ok := answers[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		ips := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			ip := net.ParseIP(a)
			if ip == nil {
				t.Fatalf("stub answer %q for %q is not an IP", a, host)
			}
			ips = append(ips, ip)
		}
		return ips, nil
	}
	return tool
}

func TestWebToolSSRFProtection(t *testing.T) {
	tool := stubResolver(t, map[string][]string{
		"example.com": {"93.184.216.34"},
	})

	tests := []struct {
		name      string
		url       string
		wantError bool
	}{
		{"localhost HTTP", "http://localhost/test", true},
		{"localhost HTTPS", "https://localhost/test", true},
		{"127.0.0.1", "http://127.0.0.1/test", true},
		{"127.0.0.1 alternative", "http://127.0.0.2/test", true},
		{"10.x private network", "http://10.0.0.1/test", true},
		{"172.16.x private network", "http://172.16.0.1/test", true},
		{"192.168.x private network", "http://192.168.1.1/test", true},
		{"AWS metadata endpoint", "http://169.254.169.254/latest/meta-data", true},
		{"valid public URL", "https://example.com/test", false},
		{"valid public URL HTTP", "http://example.com/test", false},
		{"invalid scheme", "ftp://example.com/test", true},
		{"file scheme", "file:///etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.validateURLForSSRF(tt.url)
			if tt.wantError && err == nil {
				t.Errorf("expected error for %s but got none", tt.url)
			}
			if !tt.wantError && err != nil {
				t.Errorf("expected no error for %s but got: %v", tt.url, err)
			}
		})
	}
}

// The regression this suite exists for. The old check only resolved a hostname
// when the name itself contained a keyword like "metadata" or "localhost", so
// any name an attacker controls reached any address they chose. A name carries
// no signal about where it points; only the resolved address does.
func TestSSRF_AttackerControlledHostnameIsResolved(t *testing.T) {
	cases := []struct {
		name string
		host string
		addr string
	}{
		{"loopback", "totally-innocent.example", "127.0.0.1"},
		{"cloud metadata", "fetch-my-report.example", "169.254.169.254"},
		{"private network", "cdn-assets.example", "10.0.0.5"},
		{"IPv6 loopback", "harmless.example", "::1"},
		{"IPv6 unique local", "harmless6.example", "fd00::1"},
		{"carrier NAT", "cgnat.example", "100.64.0.1"},
		{"public address is allowed", "real-site.example", "93.184.216.34"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := stubResolver(t, map[string][]string{tc.host: {tc.addr}})
			err := tool.validateURLForSSRF("http://" + tc.host + "/")

			wantBlocked := tc.addr != "93.184.216.34"
			if wantBlocked && err == nil {
				t.Fatalf("%s resolves to %s and was allowed through", tc.host, tc.addr)
			}
			if !wantBlocked && err != nil {
				t.Fatalf("public address %s was blocked: %v", tc.addr, err)
			}
		})
	}
}

// A hostname that resolves to one public and one private address must be
// refused; checking only the first answer is a bypass.
func TestSSRF_AnyPrivateAnswerBlocks(t *testing.T) {
	tool := stubResolver(t, map[string][]string{
		"mixed.example": {"93.184.216.34", "169.254.169.254"},
	})
	if err := tool.validateURLForSSRF("http://mixed.example/"); err == nil {
		t.Fatal("a hostname answering with both a public and a link-local address was allowed")
	}
}

// A lookup we cannot complete is not evidence that the target is safe.
func TestSSRF_DNSFailureIsNotTreatedAsSafe(t *testing.T) {
	tool := NewWebTool(30*time.Second, "")
	tool.resolveIP = func(string) ([]net.IP, error) {
		return nil, errors.New("server misbehaving")
	}
	err := tool.validateURLForSSRF("http://unresolvable.example/")
	if err == nil {
		t.Fatal("a failed DNS lookup was treated as safe")
	}
	if !strings.Contains(err.Error(), "DNS lookup failed") {
		t.Errorf("error should say why it could not verify; got %v", err)
	}

	// An empty answer is the same situation by a different route.
	tool.resolveIP = func(string) ([]net.IP, error) { return nil, nil }
	if err := tool.validateURLForSSRF("http://empty.example/"); err == nil {
		t.Fatal("a hostname resolving to no addresses was treated as safe")
	}
}

// The dial guard is the real enforcement point: it holds even when nothing
// called validateURLForSSRF, and it closes the rebinding window where a name
// passes the up-front check and then resolves somewhere else at connect time.
func TestDialGuardBlocksNonPublicTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret internal response"))
	}))
	defer server.Close()

	tool := NewWebTool(5*time.Second, "")

	// Straight to the client, bypassing validateURLForSSRF entirely.
	resp, err := tool.httpClient.Get(server.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("connected to %s; the dial guard did not fire", server.URL)
	}
	if !strings.Contains(err.Error(), "blocked connection to non-public address") {
		t.Errorf("connection failed for the wrong reason: %v", err)
	}
}

func TestGuardedDialControl(t *testing.T) {
	blocked := []string{
		"127.0.0.1:80", "169.254.169.254:80", "10.1.2.3:443",
		"192.168.1.1:80", "172.16.0.1:80", "[::1]:80", "[fd00::1]:80",
		"0.0.0.0:80", "100.64.0.1:80", "255.255.255.255:80",
	}
	for _, addr := range blocked {
		if err := guardedDialControl("tcp", addr, nil); err == nil {
			t.Errorf("guardedDialControl allowed %s", addr)
		}
	}

	allowed := []string{"93.184.216.34:443", "8.8.8.8:53", "[2606:4700::1111]:443"}
	for _, addr := range allowed {
		if err := guardedDialControl("tcp", addr, nil); err != nil {
			t.Errorf("guardedDialControl blocked public address %s: %v", addr, err)
		}
	}

	// Anything that is not a resolved literal is refused rather than guessed at.
	if err := guardedDialControl("tcp", "not-an-address", nil); err == nil {
		t.Error("guardedDialControl allowed an unparseable address")
	}
	if err := guardedDialControl("tcp", "example.com:80", nil); err == nil {
		t.Error("guardedDialControl allowed an unresolved hostname")
	}
}

// doSearch follows Location headers itself instead of letting the http client
// do it, so a search engine (or anyone who can answer as one) chooses the next
// URL. Before this was checked, that was an unvalidated hop.
func TestSSRF_SearchRedirectIsValidated(t *testing.T) {
	current, err := url.Parse("https://html.duckduckgo.com/html/?q=test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	tool := stubResolver(t, map[string][]string{
		"html.duckduckgo.com": {"93.184.216.34"},
		"results.example":     {"93.184.216.34"},
		"internal.example":    {"10.0.0.7"},
	})

	blocked := []struct {
		name     string
		location string
	}{
		{"cloud metadata by address", "http://169.254.169.254/latest/meta-data/"},
		{"loopback", "http://127.0.0.1/admin"},
		{"private address", "https://192.168.1.1/"},
		{"hostname resolving inside", "https://internal.example/"},
		{"internal hostname", "https://metadata.google.internal/"},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			next, err := tool.resolveSearchRedirect(current, tc.location)
			if err == nil {
				t.Fatalf("followed redirect to %s (got %s)", tc.location, next)
			}
			if !strings.Contains(err.Error(), "refusing to follow redirect") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}

	t.Run("public target is followed", func(t *testing.T) {
		next, err := tool.resolveSearchRedirect(current, "https://results.example/page/2")
		if err != nil {
			t.Fatalf("public redirect was refused: %v", err)
		}
		if next.Host != "results.example" || next.Path != "/page/2" {
			t.Errorf("redirect resolved to %s", next)
		}
	})

	t.Run("relative target keeps the current host", func(t *testing.T) {
		next, err := tool.resolveSearchRedirect(current, "/html/?q=test&p=2")
		if err != nil {
			t.Fatalf("relative redirect was refused: %v", err)
		}
		if next.Host != "html.duckduckgo.com" {
			t.Errorf("relative redirect resolved to %s", next)
		}
	})
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"127.255.255.255", true},
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"0.0.0.0", false}, // Unspecified rather than private; isBlockedIP covers it.
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			got := isPrivateIP(ip)
			if got != tt.want {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		// The one that mattered: link-local, not private, and the target of
		// essentially every cloud SSRF attack.
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"fd00::1", true},    // IPv6 unique local
		{"fe80::1", true},    // IPv6 link-local
		{"100.64.0.1", true}, // carrier-grade NAT
		{"192.0.0.1", true},  // IETF protocol assignments
		{"198.18.0.1", true}, // benchmarking
		{"240.0.0.1", true},  // reserved
		{"255.255.255.255", true},
		{"224.0.0.1", true},              // multicast
		{"::ffff:127.0.0.1", true},       // IPv4-mapped loopback
		{"::ffff:169.254.169.254", true}, // IPv4-mapped metadata
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"2606:4700:4700::1111", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("test case %q is not a valid IP", tt.ip)
			}
			if got := isBlockedIP(ip); got != tt.want {
				t.Errorf("isBlockedIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}

	if !isBlockedIP(nil) {
		t.Error("isBlockedIP(nil) should fail closed")
	}
}

func TestIsPotentiallyPrivateHostname(t *testing.T) {
	tests := []struct {
		hostname string
		want     bool
	}{
		{"localhost", true},
		{"localhost.localdomain", true},
		{"metadata.google.internal", true},
		{"169.254.169.254", true},
		{"example.com", false},
		{"api.github.com", false},
		{"kubernetes.default.svc", true},
		{"docker.internal", true},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			got := isPotentiallyPrivateHostname(tt.hostname)
			if got != tt.want {
				t.Errorf("isPotentiallyPrivateHostname(%s) = %v, want %v", tt.hostname, got, tt.want)
			}
		})
	}
}

// The headline case, kept separate so a failure names itself clearly: the GCP
// metadata hostname is in the tool's own block list and was still allowed
// through, because a failed lookup counted as safe and 169.254.0.0/16 was not
// in the private-range check.
func TestSSRF_GCPMetadataHostnameIsBlocked(t *testing.T) {
	tool := stubResolver(t, map[string][]string{
		"metadata.google.internal": {"169.254.169.254"},
	})
	if err := tool.validateURLForSSRF("http://metadata.google.internal/computeMetadata/v1/"); err == nil {
		t.Fatal("metadata.google.internal was allowed")
	}

	// Also blocked when DNS does not answer at all, which is the case on any
	// host outside GCP and the way the original bug went unnoticed.
	tool2 := stubResolver(t, nil)
	if err := tool2.validateURLForSSRF("http://metadata.google.internal/"); err == nil {
		t.Fatal("metadata.google.internal was allowed when DNS did not resolve")
	}
}

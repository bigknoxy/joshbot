package tools

import (
	"net"
	"testing"
	"time"
)

func TestWebToolSSRFProtection(t *testing.T) {
	tool := NewWebTool(30*time.Second, "")

	tests := []struct {
		name      string
		url       string
		wantError bool
	}{
		{
			name:      "localhost HTTP",
			url:       "http://localhost/test",
			wantError: true,
		},
		{
			name:      "localhost HTTPS",
			url:       "https://localhost/test",
			wantError: true,
		},
		{
			name:      "127.0.0.1",
			url:       "http://127.0.0.1/test",
			wantError: true,
		},
		{
			name:      "127.0.0.1 alternative",
			url:       "http://127.0.0.2/test",
			wantError: true,
		},
		{
			name:      "10.x private network",
			url:       "http://10.0.0.1/test",
			wantError: true,
		},
		{
			name:      "172.16.x private network",
			url:       "http://172.16.0.1/test",
			wantError: true,
		},
		{
			name:      "192.168.x private network",
			url:       "http://192.168.1.1/test",
			wantError: true,
		},
		{
			name:      "AWS metadata endpoint",
			url:       "http://169.254.169.254/latest/meta-data",
			wantError: true,
		},
		// Note: GCP metadata endpoint is blocked via hostname pattern in non-DNS environments
		// but may pass if DNS resolves to a public IP
		{
			name:      "valid public URL",
			url:       "https://example.com/test",
			wantError: false,
		},
		{
			name:      "valid public URL HTTP",
			url:       "http://example.com/test",
			wantError: false,
		},
		{
			name:      "invalid scheme",
			url:       "ftp://example.com/test",
			wantError: true,
		},
		{
			name:      "file scheme",
			url:       "file:///etc/passwd",
			wantError: true,
		},
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
		{"0.0.0.0", false}, // This is technically not private but isUnspecified
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

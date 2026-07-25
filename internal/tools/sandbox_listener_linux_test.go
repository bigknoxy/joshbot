//go:build linux

package tools

import (
	"net"
	"net/http"
	"testing"
)

// startLocalListener runs a plain HTTP server on loopback and returns its
// address. A local listener is used rather than a public host so the network
// assertion tests the sandbox rather than whether CI has internet — an
// earlier version of this check passed for the wrong reason because the
// environment had no DNS at all.
func startLocalListener(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return ln.Addr().String()
}

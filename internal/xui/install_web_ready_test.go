package xui

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebReadinessVerifiesTLSAndExactPageWithoutFollowingRedirects(t *testing.T) {
	for _, scenario := range []string{"ready", "untrusted", "wrong-page", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "wrong-page":
					w.Write([]byte("other page"))
				case "redirect":
					http.Redirect(w, r, "https://outside.example.test", http.StatusFound)
				default:
					w.Write([]byte("reviewed page"))
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			if scenario == "untrusted" {
				roots = x509.NewCertPool()
			}
			err := probeDecoy(t.Context(), "example.com", server.Listener.Addr().String(), []byte("reviewed page"), roots)
			if (err == nil) != (scenario == "ready") {
				t.Fatal("HTTPS readiness accepted an untrusted certificate, changed page or redirect")
			}
		})
	}
	if err := probeDecoy(t.Context(), "example.com", "192.0.2.1:2053", []byte("reviewed page"), nil); err == nil {
		t.Fatal("readiness probe accepted non-local dial target")
	}
}

package xui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPromotedReadinessWaitsForRouteAndCoreWithoutRepeatedLogin(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			var csrf, logins, status atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/csrf-token"):
					if csrf.Add(1) == 1 {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fmt.Fprint(w, `{"success":true,"obj":"test-csrf"}`)
				case strings.HasSuffix(r.URL.Path, "/getTwoFactorEnable"):
					fmt.Fprint(w, `{"success":true,"obj":false}`)
				case strings.HasSuffix(r.URL.Path, "/login"):
					logins.Add(1)
					fmt.Fprintf(w, `{"success":%t,"obj":null}`, !reject)
				case strings.HasSuffix(r.URL.Path, "/server/status"):
					state := "running"
					if status.Add(1) == 1 {
						state = "stopped"
					}
					fmt.Fprintf(w, `{"success":true,"obj":{"xray":{"state":%q,"errorMsg":""}}}`, state)
				default:
					http.NotFound(w, r)
				}
			}))
			defer h.Close()
			p, err := NewPanel(h.URL + "/test-panel")
			if err != nil {
				t.Fatal(err)
			}
			defer p.transport.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			s, err := waitPromotedPanel(ctx, p, config{Env: map[string]string{"PANEL_USER": "test", "PANEL_PASS": "test-password"}})
			if logins.Load() != 1 {
				t.Fatal("readiness repeated authenticated login")
			}
			if reject {
				if !errors.Is(err, errCredentials) {
					t.Fatal("rejected credentials were retried")
				}
			} else if err != nil || s == nil || status.Load() < 2 {
				t.Fatal("route/core readiness did not recover")
			}
		})
	}
}

func TestPrivateHTTPReadinessRetriesReadsBeforeBootstrap(t *testing.T) {
	var requests, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			posts.Add(1)
		}
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"success":true,"obj":"test-token"}`)
	}))
	defer server.Close()
	p, err := NewPanel(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer p.transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := waitPanelHTTP(ctx, p); err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 3 || posts.Load() != 0 {
		t.Fatal("readiness retried writes or skipped the delayed endpoint")
	}
}

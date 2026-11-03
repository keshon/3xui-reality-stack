package xui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func bootstrapFixture(t *testing.T, mode string, configurations ...config) (*panelBootstrap, *Panel, func() *Panel, *int, *int) {
	t.Helper()
	c := config{Env: map[string]string{
		"PANEL_USER": "private-admin", "PANEL_PASS": newPassword,
		"PANEL_PATH": "/private-panel", "SUB_PATH": "/private-sub", "JSON_PATH": "/private-json",
		"PUBLIC_DOMAIN": "example.test",
	}}
	if len(configurations) != 0 {
		c = configurations[0]
	}
	b, err := newPanelBootstrap(c)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]json.RawMessage{}
	for key, value := range b.settings {
		settings[key] = value
	}
	settings["webBasePath"] = json.RawMessage(`"/"`)
	settings["sessionMaxAge"] = json.RawMessage(`360`)
	settings["hasTgBotToken"] = json.RawMessage(`true`)
	settings["tgBotToken"] = json.RawMessage(`""`)
	settings["futureSetting"] = json.RawMessage(`{"preserve":[1234567890123456789,true]}`)
	if mode == "incomplete" {
		delete(settings, "subJsonURI")
	}
	username, password, base := "admin", "admin", ""
	if mode == "foreign" {
		username, password = "foreign-admin", "foreign-password"
	}
	sessions := map[string]bool{}
	var mu sync.Mutex
	credentialWrites, settingsWrites := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, base)
		reply := func(ok bool, value any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "obj": value}) }
		drop := func() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
		if base != "" && !strings.HasPrefix(r.URL.Path, base+"/") {
			http.NotFound(w, r)
			return
		}
		if path == "/csrf-token" && r.Method == "GET" {
			http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "bound", Path: "/"})
			reply(true, "csrf-token")
			return
		}
		if r.Method != "POST" || r.Header.Get("X-CSRF-TOKEN") != "csrf-token" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "invalid request", 403)
			return
		}
		if path == "/getTwoFactorEnable" {
			reply(true, mode == "two-factor")
			return
		}
		if path == "/login" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if mode == "unavailable" && body["username"] == b.username {
				http.Error(w, "unavailable", 503)
				return
			}
			ok := body["username"] == username && body["password"] == password
			if ok {
				id := fmt.Sprintf("session-%d", len(sessions))
				sessions[id] = true
				http.SetCookie(w, &http.Cookie{Name: "session", Value: id, Path: "/"})
			}
			reply(ok, nil)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil || !sessions[cookie.Value] {
			http.Error(w, "no session", 401)
			return
		}
		switch path {
		case "/panel/api/setting/updateUser":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["oldUsername"] != username || body["oldPassword"] != password || body["newUsername"] != b.username || body["newPassword"] != b.password {
				t.Error("credential body did not match the pinned API contract")
				reply(false, nil)
				return
			}
			credentialWrites++
			if mode == "reject-credentials" {
				reply(false, nil)
				return
			}
			username, password = body["newUsername"], body["newPassword"]
			if mode == "lost-credentials" {
				drop()
				return
			}
			reply(true, nil)
		case "/panel/api/setting/all":
			reply(true, settings)
		case "/panel/api/setting/update":
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, key := range []string{"futureSetting", "sessionMaxAge", "hasTgBotToken", "tgBotToken"} {
				if !reflect.DeepEqual(body[key], settings[key]) {
					t.Error("unrelated setting was changed or omitted")
				}
			}
			settingsWrites++
			if mode != "ignore-settings" {
				settings = body
			}
			if mode == "alter-unrelated" {
				settings["futureSetting"] = json.RawMessage(`null`)
			}
			if mode == "lost-settings" {
				drop()
				return
			}
			reply(true, nil)
		default:
			t.Errorf("unexpected bootstrap endpoint %s", path)
			http.NotFound(w, r)
		}
	}))
	open := func(path string) *Panel {
		p, err := NewPanel(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.transport.CloseIdleConnections)
		return p
	}
	t.Cleanup(server.Close)
	restart := func() *Panel {
		mu.Lock()
		base = strings.TrimRight(c.Env["PANEL_PATH"], "/")
		sessions = map[string]bool{}
		mu.Unlock()
		return open(base)
	}
	return b, open(""), restart, &credentialWrites, &settingsWrites
}

func TestPanelBootstrapRecovery(t *testing.T) {
	for _, mode := range []string{"normal", "lost-credentials", "lost-settings"} {
		t.Run(mode, func(t *testing.T) {
			b, p, restart, credentials, settings := bootstrapFixture(t, mode)
			ctx := context.Background()
			for range 2 {
				if err := b.prepare(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			if *credentials != 1 || *settings != 1 {
				t.Fatal("recovery repeated committed initialization writes")
			}
			if b.verify(ctx, p) == nil {
				t.Fatal("persisted settings were mistaken for listener readiness")
			}
			configured := restart()
			if err := b.verify(ctx, configured); err != nil {
				t.Fatal(err)
			}
			if err := b.prepare(ctx, configured); err != nil || *credentials != 1 || *settings != 1 {
				t.Fatal("post-restart replay did not preserve configured state")
			}
		})
	}
}

func TestPanelBootstrapRefusesUnprovenState(t *testing.T) {
	for _, mode := range []string{"foreign", "unavailable", "two-factor", "reject-credentials", "ignore-settings", "alter-unrelated", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			b, p, _, credentials, settings := bootstrapFixture(t, mode)
			if err := b.prepare(context.Background(), p); err == nil {
				t.Fatal("unsafe or unverifiable bootstrap succeeded")
			}
			if (mode == "foreign" || mode == "unavailable" || mode == "two-factor") && (*credentials != 0 || *settings != 0) {
				t.Fatal("authentication failure caused bootstrap writes")
			}
		})
	}
	b, _, _, _, _ := bootstrapFixture(t, "normal")
	remote, _ := NewPanel("https://example.test/private-panel")
	if b.prepare(context.Background(), remote) == nil || b.verify(context.Background(), remote) == nil {
		t.Fatal("public endpoint was accepted for private bootstrap")
	}
}

func TestPanelBootstrapRejectsInvalidConfiguration(t *testing.T) {
	for _, key := range []string{"PANEL_USER", "PANEL_PASS", "PUBLIC_DOMAIN", "PANEL_PATH", "SUB_PATH", "JSON_PATH"} {
		t.Run(key, func(t *testing.T) {
			e := map[string]string{
				"PANEL_USER": "private-admin", "PANEL_PASS": newPassword, "PUBLIC_DOMAIN": "example.test",
				"PANEL_PATH": "/private-panel", "SUB_PATH": "/private-sub", "JSON_PATH": "/private-json",
			}
			e[key] = ""
			if _, err := newPanelBootstrap(config{Env: e}); err == nil {
				t.Fatal("incomplete bootstrap configuration was accepted")
			}
		})
	}
}

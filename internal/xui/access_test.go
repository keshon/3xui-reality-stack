package xui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestAccessUsesAppliedCredentialsAndExactPanelPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "stack")
	s := Stack{}
	if _, err := s.Initialize(t.Context(), root, rawInitialInput(t, validInitialInput())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access(t.Context(), root); err == nil {
		t.Fatal("unapplied draft offered working credentials")
	}
	var c config
	if ok, err := readJSON(filepath.Join(root, ".stack/config.json"), &c, true); err != nil || !ok {
		t.Fatal("cannot read fixture")
	}
	c.Env["CF_DNS_API_TOKEN"] = "never-send-cloudflare-token"
	c.Env["PANEL_PASS"] = "applied-password"
	if err := deployment.WriteJSON(filepath.Join(root, ".stack/applied.json"), baseline{Version: 1, Config: c}); err != nil {
		t.Fatal(err)
	}
	c.Env["PANEL_PASS"] = "pending-desired-password"
	if err := deployment.WriteJSON(filepath.Join(root, ".stack/config.json"), c); err != nil {
		t.Fatal(err)
	}
	r, err := s.Access(t.Context(), root)
	if err != nil || r.Links[0].Value != "https://ops.example.test"+c.Env["PANEL_PATH"]+"/" || r.Credentials[1].Value != "applied-password" || !r.Credentials[1].Secret {
		t.Fatal("access details lost the applied identity or generated path")
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "never-send-cloudflare-token") || strings.Contains(string(raw), "pending-desired-password") {
		t.Fatal("access details included unrelated or unapplied secrets")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Access(ctx, root); err == nil {
		t.Fatal("cancelled access read succeeded")
	}
	if err := os.WriteFile(filepath.Join(root, ".stack/applied.json"), []byte(`{"private":"do-not-quote-this"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access(t.Context(), root); err == nil || strings.Contains(err.Error(), "do-not-quote-this") {
		t.Fatal("invalid applied state was accepted or quoted")
	}
}

package xui

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// Access uses the applied configuration: a pending desired password must never
// be presented as a working login. It makes no panel or Docker requests.
func (Stack) Access(ctx context.Context, root string) (deployment.AccessReport, error) {
	if err := ctx.Err(); err != nil {
		return deployment.AccessReport{}, err
	}
	fail := func() (deployment.AccessReport, error) {
		return deployment.AccessReport{}, errors.New("applied access details are unavailable")
	}
	if err := (deployment.Engine{Root: root}).CheckIdle(); err != nil {
		return fail()
	}
	data, err := privatePromotionFile(filepath.Join(root, ".stack"), "applied.json", 4<<20)
	var b baseline
	if err != nil || json.Unmarshal(data, &b) != nil || b.Version != 1 || !valid(b.Config) {
		return fail()
	}
	e := b.Config.Env
	if !initialDomain(e["PANEL_DOMAIN"]) || !initialDomain(e["PUBLIC_DOMAIN"]) || !panelPath(e["PANEL_PATH"]) || e["PANEL_USER"] == "" || e["PANEL_PASS"] == "" || controls(e["PANEL_USER"]) || controls(e["PANEL_PASS"]) {
		return fail()
	}
	return deployment.AccessReport{
		Stack:        stackID,
		Links:        []deployment.AccessValue{{Label: "Panel", Value: "https://" + e["PANEL_DOMAIN"] + e["PANEL_PATH"] + "/"}, {Label: "Public website", Value: "https://" + e["PUBLIC_DOMAIN"] + "/"}},
		Credentials:  []deployment.AccessValue{{Label: "Admin username", Value: e["PANEL_USER"]}, {Label: "Admin password", Value: e["PANEL_PASS"], Secret: true}},
		ConfigFile:   filepath.Join(root, ".env"),
		Instructions: []string{"Open the full panel URL, including its generated path, and sign in.", "In the panel, open Clients to copy a user's subscription URL into HAPP.", "Test Reality, Trojan and Hysteria2 with an actual website. Keep Mux and fragmentation off for Reality.", "These are applied access details; they do not verify connectivity from your device."},
	}, nil
}

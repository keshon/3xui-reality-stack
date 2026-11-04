package xui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"reflect"
	"strings"
)

// panelBootstrap is only for a freshly created, operation-owned database whose
// container has no public listener or enabled Traefik routes. The caller must
// establish that ownership before calling prepare. Loopback alone is not proof
// of ownership. This helper is deliberately absent from the public controller.
type panelBootstrap struct {
	username, password string
	settings           map[string]json.RawMessage
}

func newPanelBootstrap(c config) (*panelBootstrap, error) {
	e := c.Env
	if e["PANEL_USER"] == "" || e["PANEL_PASS"] == "" || e["PANEL_PASS"] == "admin" || !initialDomain(e["PUBLIC_DOMAIN"]) {
		return nil, errors.New("invalid private panel bootstrap credentials or domain")
	}
	for _, key := range []string{"PANEL_PATH", "SUB_PATH", "JSON_PATH"} {
		if !panelPath(e[key]) {
			return nil, errors.New("invalid private panel bootstrap paths")
		}
	}
	path := func(key string) string { return "/" + strings.Trim(e[key], "/") + "/" }
	values := map[string]any{
		"webBasePath": path("PANEL_PATH"), "webListen": "", "webPort": 2053,
		"webDomain": "", "webCertFile": "", "webKeyFile": "",
		"subEnable": true, "subJsonEnable": true,
		"subListen": "", "subPort": 2096, "subDomain": "", "subCertFile": "", "subKeyFile": "",
		"subPath": path("SUB_PATH"), "subJsonPath": path("JSON_PATH"),
		"subURI":     "https://" + e["PUBLIC_DOMAIN"] + path("SUB_PATH"),
		"subJsonURI": "https://" + e["PUBLIC_DOMAIN"] + path("JSON_PATH"),
	}
	b := &panelBootstrap{username: e["PANEL_USER"], password: e["PANEL_PASS"], settings: map[string]json.RawMessage{}}
	for key, value := range values {
		b.settings[key], _ = json.Marshal(value)
	}
	return b, nil
}

func privateBootstrapEndpoint(p *Panel) bool {
	if p == nil {
		return false
	}
	u, err := url.Parse(p.base)
	return err == nil && net.ParseIP(u.Hostname()).IsLoopback()
}

// prepare changes credentials through HTTP bodies, then round-trips the complete
// settings view. It does not restart the panel or expose routes. Persisted
// settings and listener readiness are distinct: the owner must restart, reopen
// at the desired base path, and call verify before publishing anything.
func (b *panelBootstrap) prepare(ctx context.Context, p *Panel) error {
	if !privateBootstrapEndpoint(p) {
		return errors.New("panel bootstrap requires an isolated loopback endpoint")
	}
	session, err := p.login(ctx, b.username, b.password)
	if errors.Is(err, errCredentials) {
		session, err = p.login(ctx, "admin", "admin")
		if err != nil {
			return err
		}
		writeErr := session.changeCredentials(ctx, "admin", "admin", b.username, b.password)
		// A lost response may follow a committed credential write. Always prove
		// the desired credentials with a new session before deciding to retry.
		session, err = p.login(ctx, b.username, b.password)
		if err != nil {
			if writeErr != nil {
				return errors.New("panel credential initialization needs recovery")
			}
			return errors.New("initialized panel credentials could not be verified")
		}
	}
	if err != nil {
		return err
	}
	current, err := session.settings(ctx)
	if err != nil {
		return err
	}
	for key := range b.settings {
		if _, ok := current[key]; !ok {
			return errors.New("panel bootstrap settings contract is incomplete")
		}
	}
	if current["sessionMaxAge"] == nil {
		return errors.New("panel bootstrap settings contract is incomplete")
	}
	if settingsContain(current, b.settings) {
		return nil
	}
	// Upstream binds a full AllSetting, not a patch. Retain unrelated fields
	// and redacted-secret presence flags; never send explicit secret clears.
	for key, value := range b.settings {
		current[key] = value
	}
	writeErr := session.json(ctx, "/panel/api/setting/update", current, nil)
	observed, err := session.settings(ctx)
	if err != nil || !settingsContain(observed, current) {
		if writeErr != nil {
			return errors.New("panel settings initialization needs recovery")
		}
		return errors.New("initialized panel settings could not be verified")
	}
	return nil
}

func (b *panelBootstrap) verify(ctx context.Context, p *Panel) error {
	if !privateBootstrapEndpoint(p) {
		return errors.New("panel bootstrap requires an isolated loopback endpoint")
	}
	u, _ := url.Parse(p.base)
	var desiredPath string
	_ = json.Unmarshal(b.settings["webBasePath"], &desiredPath)
	if strings.TrimRight(u.Path, "/") != strings.TrimRight(desiredPath, "/") {
		return errors.New("panel readiness requires the configured access path")
	}
	session, err := p.login(ctx, b.username, b.password)
	if err != nil {
		return err
	}
	current, err := session.settings(ctx)
	if err != nil || !settingsContain(current, b.settings) {
		return errors.New("panel bootstrap readiness verification failed")
	}
	return nil
}

func (s *panelSession) settings(ctx context.Context) (map[string]json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := s.json(ctx, "/panel/api/setting/all", map[string]string{}, &result); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("panel settings view is empty")
	}
	return result, nil
}

func settingsContain(observed, expected map[string]json.RawMessage) bool {
	for key, value := range expected {
		a, errA := canonicalJSON(observed[key])
		b, errB := canonicalJSON(value)
		if errA != nil || errB != nil || !reflect.DeepEqual(a, b) {
			return false
		}
	}
	return true
}

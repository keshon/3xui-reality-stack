package xui

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

func readNativeSubscription(ctx context.Context, c config, subID string) ([]byte, error) {
	env := make(map[string]string, len(c.Env))
	for k, v := range c.Env {
		env[k] = v
	}
	env["PANEL_DOMAIN"], env["PANEL_PATH"] = env["PUBLIC_DOMAIN"], env["SUB_PATH"]
	c.Env = env
	p, err := managedConfigurationPanel(c)
	if err != nil {
		return nil, err
	}
	defer p.transport.CloseIdleConnections()
	return readSubscriptionEndpoint(ctx, p, subID)
}

func readSubscriptionEndpoint(ctx context.Context, p *Panel, subID string) ([]byte, error) {
	// Use a new unauthenticated client: administrator cookies never reach the
	// subscription endpoint. Production transport dials only local Traefik with
	// the public subscription hostname as Host/SNI and system certificate trust.
	s := &panelSession{base: p.base, http: &http.Client{Transport: p.transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return s.request(ctx, "/"+url.PathEscape(subID), nil, 8<<20)
}

func validateNativeSubscription(data []byte, c config, p *provisioningIntent, client provisionedClient) error {
	fail := func() error { return errors.New("subscription does not match the provisioned account and transports") }
	if len(data) > 8<<20 {
		return fail()
	}
	var decoded []byte
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if result, err := encoding.DecodeString(strings.TrimSpace(string(data))); err == nil {
			decoded = result
			break
		}
	}
	if len(decoded) == 0 || !utf8.Valid(decoded) {
		return fail()
	}
	private, err := base64.RawURLEncoding.DecodeString(p.PrivateKey)
	if err != nil {
		return fail()
	}
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return fail()
	}
	public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	seen := map[string]bool{}
	for _, line := range strings.Split(string(decoded), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.User == nil || u.Opaque != "" || u.Hostname() != c.Env["PUBLIC_DOMAIN"] || (u.Port() != "" && u.Port() != "443") {
			return fail()
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return fail()
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return fail()
		}
		for _, values := range q {
			if len(values) != 1 {
				return fail()
			}
		}
		for _, name := range []string{"insecure", "allowInsecure", "skip-cert-verify"} {
			v := strings.ToLower(q.Get(name))
			if v != "" && v != "0" && v != "false" {
				return fail()
			}
		}
		if sni := q.Get("sni"); sni != "" && sni != c.Env["PUBLIC_DOMAIN"] {
			return fail()
		}
		lane := u.Scheme
		switch lane {
		case "vless":
			if u.User.Username() != client.ID || q.Get("security") != "reality" || (q.Get("type") != "tcp" && q.Get("type") != "raw") || q.Get("encryption") != "none" || q.Get("flow") != "xtls-rprx-vision" || q.Get("pbk") != public || q.Get("sid") != p.ShortID || q.Get("fp") != c.Env["REALITY_FINGERPRINT"] || q.Get("sni") != c.Env["PUBLIC_DOMAIN"] {
				return fail()
			}
		case "trojan":
			if u.User.Username() != client.Password || q.Get("type") != "httpupgrade" || q.Get("security") != "tls" || q.Get("path") != c.Env["TROJAN_PATH"] || q.Get("host") != c.Env["PUBLIC_DOMAIN"] {
				return fail()
			}
		case "hy2", "hysteria2":
			lane = "hysteria2"
			if u.User.Username() != client.Auth || q.Get("security") != "tls" || q.Get("alpn") != "h3" || q.Get("obfs") != "" {
				return fail()
			}
		default:
			return fail()
		}
		if seen[lane] {
			return fail()
		}
		seen[lane] = true
	}
	if len(seen) != 3 {
		return fail()
	}
	return nil
}

func (w *installRuntime) verifyNativeSubscriptions(ctx context.Context, dir string, snapshot installRuntimeSnapshot) error {
	return w.verifySubscriptions(ctx, dir, snapshot, false)
}

func (w *installRuntime) verifyInstallation(ctx context.Context, dir string, snapshot installRuntimeSnapshot) error {
	if err := w.promotePanel(ctx, dir, snapshot); err != nil {
		return err
	}
	return w.verifySubscriptions(ctx, dir, snapshot, true)
}

func (w *installRuntime) verifySubscriptions(ctx context.Context, dir string, snapshot installRuntimeSnapshot, transfers bool) error {
	var c config
	if json.Unmarshal(snapshot.Binding.Inputs[".stack/config.json"], &c) != nil {
		return errors.New("invalid frozen subscription configuration")
	}
	web, err := w.webIntent(ctx, dir, snapshot)
	if err != nil {
		return err
	}
	panel, err := w.panelIntent(ctx, dir, snapshot, web)
	if err != nil {
		return err
	}
	intent, err := w.promotionIntent(ctx, dir, snapshot, web, panel, c)
	if err != nil {
		return err
	}
	guard := func() error {
		found, err := w.observeRuntimeServices(ctx, snapshot, web, dir, &panel, &intent)
		if err != nil {
			return err
		}
		if !found["3x-ui"].Running || found[panelContainerName(snapshot.Owner)].Running {
			return errors.New("subscription verification requires the promoted panel")
		}
		var saved webIdentity
		if ok, err := readJSON(filepath.Join(dir, "panel-published.json"), &saved, true); err != nil || !ok {
			return errors.New("published panel identity receipt unavailable")
		}
		current := found["3x-ui"]
		if saved.ID != current.ID || saved.ImageID != current.ImageID || saved.StartedAt != current.StartedAt {
			return errors.New("published subscription target changed")
		}
		return nil
	}
	if err := guard(); err != nil {
		return err
	}
	data, err := privatePromotionFile(dir, "provisioning.json", 2<<20)
	if err != nil {
		return err
	}
	var plan provisioningIntent
	if json.Unmarshal(data, &plan) != nil {
		return errors.New("invalid frozen subscription identities")
	}
	if _, err := plan.inbounds(c); err != nil {
		return err
	}
	if plan.Pending != "" || len(plan.AppliedClients) != len(c.Users) || len(plan.InboundIDs) != 3 {
		return errors.New("subscription identities are not fully provisioned")
	}
	if transfers {
		open := w.publishedPanel
		if open == nil {
			open = managedConfigurationPanel
		}
		p, err := open(c)
		if err != nil {
			return err
		}
		defer p.transport.CloseIdleConnections()
		s, err := p.login(ctx, c.Env["PANEL_USER"], c.Env["PANEL_PASS"])
		if err != nil {
			return err
		}
		if c.Env["RU_POLICY"] != "allow" {
			return errors.New("fresh installation requires the unrestricted regional policy")
		}
		if err := verifyFreshRouting(ctx, s); err != nil {
			return err
		}
	}
	read := w.subscriptionRead
	if read == nil {
		read = readNativeSubscription
	}
	for index, client := range plan.Clients {
		if err := guard(); err != nil {
			return err
		}
		body, err := read(ctx, c, client.SubID)
		if err != nil {
			return errors.New("published subscription request failed")
		}
		if err := validateNativeSubscription(body, c, &plan, client); err != nil {
			return err
		}
		if transfers {
			jsonReader := w.jsonRead
			if jsonReader == nil {
				jsonReader = func(ctx context.Context, c config, id string) ([]byte, error) {
					env := make(map[string]string, len(c.Env))
					for k, v := range c.Env {
						env[k] = v
					}
					env["SUB_PATH"] = env["JSON_PATH"]
					c.Env = env
					return readNativeSubscription(ctx, c, id)
				}
			}
			if err := guard(); err != nil {
				return err
			}
			data, err := jsonReader(ctx, c, client.SubID)
			if err != nil {
				return errors.New("published JSON subscription request failed")
			}
			outbounds, err := checkedJSONOutbounds(data, c, &plan, client)
			if err != nil {
				return err
			}
			// Check every account's profiles, then transfer through each transport
			// using one existing account. No temporary panel users are created.
			if index == 0 {
				probe := w.transferProbe
				if probe == nil {
					probe = probePublishedTransfer
				}
				var identity webIdentity
				if ok, err := readJSON(filepath.Join(dir, "panel-published.json"), &identity, true); err != nil || !ok {
					return errors.New("published transfer target unavailable")
				}
				for _, outbound := range outbounds {
					if err := guard(); err != nil {
						return err
					}
					input, _ := json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": 39080, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []json.RawMessage{outbound}})
					if err := probe(ctx, identity.ID, snapshot.Binding.Host.Architecture, input); err != nil {
						return errors.New("published transport failed its authenticated transfer check")
					}
				}
			}
		}
	}
	return guard()
}

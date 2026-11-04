package xui

import (
	"context"
	"crypto/ecdh"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

//go:embed probe-transfer.sh
var transferScript string

func probePublishedTransfer(ctx context.Context, id, arch string, data []byte) error {
	if !dockerID(id) || arch != "amd64" && arch != "arm64" {
		return errors.New("invalid authenticated transfer target")
	}
	output, err := localDockerInput(ctx, 50*time.Second, data, "exec", "-i", id, "sh", "-c", transferScript, "sh", arch)
	fields := strings.Fields(string(output))
	if err != nil || len(fields) != 2 || fields[0] != "200" {
		return errors.New("authenticated transfer failed")
	}
	size, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || size < 65536 {
		return errors.New("authenticated transfer did not meet the byte threshold")
	}
	return ctx.Err()
}

// Match the connection fields emitted by the pinned panel, allowing its
// descriptive/default fields. Native links are checked independently.
func expectedSubscriptionOutbounds(c config, plan *provisioningIntent, client provisionedClient) map[string]map[string]any {
	private, _ := base64.RawURLEncoding.DecodeString(plan.PrivateKey)
	key, _ := ecdh.X25519().NewPrivateKey(private) // caller validated plan.inbounds
	host := c.Env["PUBLIC_DOMAIN"]
	return map[string]map[string]any{
		"vless":    {"protocol": "vless", "tag": "proxy", "settings": map[string]any{"address": host, "port": 443, "id": client.ID, "encryption": "none", "flow": "xtls-rprx-vision"}, "streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": host, "fingerprint": c.Env["REALITY_FINGERPRINT"], "publicKey": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "shortId": plan.ShortID}}},
		"trojan":   {"protocol": "trojan", "tag": "proxy", "settings": map[string]any{"servers": []any{map[string]any{"address": host, "port": 443, "password": client.Password, "level": 8}}}, "streamSettings": map[string]any{"network": "httpupgrade", "security": "tls", "httpupgradeSettings": map[string]any{"host": host, "path": c.Env["TROJAN_PATH"]}}},
		"hysteria": {"protocol": "hysteria", "tag": "proxy", "settings": map[string]any{"address": host, "port": 443, "version": 2}, "streamSettings": map[string]any{"network": "hysteria", "security": "tls", "tlsSettings": map[string]any{"serverName": host, "alpn": []string{"h3"}}, "hysteriaSettings": map[string]any{"version": 2, "auth": client.Auth}}},
	}
}

func checkedJSONOutbounds(data []byte, c config, plan *provisioningIntent, client provisionedClient) ([][]byte, error) {
	fail := func() ([][]byte, error) {
		return nil, errors.New("JSON subscription does not match the provisioned transports")
	}
	var documents []struct {
		Outbounds []json.RawMessage `json:"outbounds"`
		Routing   json.RawMessage   `json:"routing"`
	}
	if len(data) > 8<<20 || json.Unmarshal(data, &documents) != nil || len(documents) != 3 {
		return fail()
	}
	wanted := expectedSubscriptionOutbounds(c, plan, client)
	var result [][]byte
	for _, doc := range documents {
		if len(doc.Outbounds) != 3 || !containsProvisioningJSON(string(doc.Routing), `{"domainStrategy":"AsIs","rules":[{"type":"field","network":"tcp,udp","outboundTag":"proxy"}]}`, false) {
			return fail()
		}
		var outbound struct {
			Protocol string `json:"protocol"`
			Mux      struct {
				Enabled bool `json:"enabled"`
			} `json:"mux"`
			Stream struct {
				TLS struct {
					Insecure   bool   `json:"allowInsecure"`
					ServerName string `json:"serverName"`
				} `json:"tlsSettings"`
			} `json:"streamSettings"`
		}
		if json.Unmarshal(doc.Outbounds[0], &outbound) != nil || wanted[outbound.Protocol] == nil || outbound.Mux.Enabled || outbound.Stream.TLS.Insecure {
			return fail()
		}
		expected, _ := json.Marshal(wanted[outbound.Protocol])
		if !containsProvisioningJSON(string(doc.Outbounds[0]), string(expected), false) {
			return fail()
		}
		if outbound.Protocol == "trojan" && outbound.Stream.TLS.ServerName != "" && outbound.Stream.TLS.ServerName != c.Env["PUBLIC_DOMAIN"] {
			return fail()
		}
		for i, protocol := range []string{"freedom", "blackhole"} {
			var extra struct {
				Protocol string `json:"protocol"`
			}
			if json.Unmarshal(doc.Outbounds[i+1], &extra) != nil || extra.Protocol != protocol {
				return fail()
			}
		}
		result = append(result, doc.Outbounds[0])
		delete(wanted, outbound.Protocol)
	}
	if len(wanted) != 0 {
		return fail()
	}
	return result, nil
}

// Fresh installations retain the pinned panel's private-address and BitTorrent
// protections. Regional/domain deny rules are not accepted by this installer.
const freshRouting = `{"domainStrategy":"AsIs","rules":[{"inboundTag":["api"],"outboundTag":"api","type":"field"},{"ip":["geoip:private"],"outboundTag":"blocked","type":"field"},{"outboundTag":"blocked","protocol":["bittorrent"],"type":"field"}]}`
const freshOutbounds = `[{"protocol":"freedom","settings":{"domainStrategy":"AsIs","finalRules":[{"action":"block","ip":["geoip:private"]},{"action":"allow"}]},"tag":"direct"},{"protocol":"blackhole","settings":{},"tag":"blocked"}]`

func verifyFreshRouting(ctx context.Context, s *panelSession) error {
	var current struct {
		Routing   json.RawMessage `json:"routing"`
		Outbounds json.RawMessage `json:"outbounds"`
	}
	if err := s.json(ctx, "/panel/api/server/getConfigJson", nil, &current); err != nil {
		return err
	}
	if !containsProvisioningJSON(string(current.Routing), freshRouting, false) || !containsProvisioningJSON(string(current.Outbounds), freshOutbounds, false) {
		return errors.New("fresh panel routing differs from the pinned unrestricted regional policy")
	}
	return nil
}

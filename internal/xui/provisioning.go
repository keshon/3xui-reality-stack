package xui

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// provisioningIntent is private operation state, never a report or RPC payload.
// Applied records server-assigned IDs without changing any generated identity.
type provisioningIntent struct {
	Version        int                 `json:"version"`
	ConfigHash     string              `json:"config_hash"`
	PrivateKey     string              `json:"private_key"`
	ShortID        string              `json:"short_id"`
	Clients        []provisionedClient `json:"clients"`
	InboundIDs     map[string]int      `json:"inbound_ids"`
	AppliedClients map[string]bool     `json:"applied_clients"`
	Pending        string              `json:"pending"`
}

type provisionedClient struct {
	Email      string `json:"email"`
	ID         string `json:"id"`
	Password   string `json:"password"`
	Auth       string `json:"auth"`
	SubID      string `json:"subId"`
	Flow       string `json:"flow"`
	Enable     bool   `json:"enable"`
	LimitIP    int    `json:"limitIp"`
	TotalGB    int64  `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
}

type provisionedInbound struct {
	ID             int    `json:"id,omitempty"`
	Remark         string `json:"remark"`
	Enable         bool   `json:"enable"`
	Listen         string `json:"listen"`
	Port           int    `json:"port"`
	Protocol       string `json:"protocol"`
	NodeID         *int   `json:"nodeId,omitempty"`
	Settings       string `json:"settings"`
	StreamSettings string `json:"streamSettings"`
	Sniffing       string `json:"sniffing"`
	Allocate       string `json:"allocate"`
}

func provisioningHash(c config) string {
	data, _ := json.Marshal(c)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func newProvisioningIntent(c config, entropy io.Reader) (*provisioningIntent, error) {
	if _, err := renderDeployment(c); err != nil {
		return nil, err
	}
	if len(c.Users) == 0 || len(c.Users) > 1000 {
		return nil, errors.New("invalid provisioning accounts")
	}
	seen := map[string]bool{}
	for _, name := range c.Users {
		if !accountName.MatchString(name) || seen[name] {
			return nil, errors.New("invalid provisioning accounts")
		}
		seen[name] = true
	}
	key, err := ecdh.X25519().GenerateKey(entropy)
	if err != nil {
		return nil, errors.New("cannot generate provisioning credentials")
	}
	token := func(size int) (string, error) {
		data := make([]byte, size)
		if _, err := io.ReadFull(entropy, data); err != nil {
			return "", errors.New("cannot generate provisioning credentials")
		}
		return hex.EncodeToString(data), nil
	}
	short, err := token(8)
	if err != nil {
		return nil, err
	}
	p := &provisioningIntent{Version: 1, ConfigHash: provisioningHash(c), PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), ShortID: short, Clients: []provisionedClient{}, InboundIDs: map[string]int{}, AppliedClients: map[string]bool{}}
	for _, name := range c.Users {
		data := make([]byte, 16)
		if _, err := io.ReadFull(entropy, data); err != nil {
			return nil, errors.New("cannot generate provisioning credentials")
		}
		data[6] = (data[6] & 0x0f) | 0x40
		data[8] = (data[8] & 0x3f) | 0x80
		id := fmt.Sprintf("%x-%x-%x-%x-%x", data[:4], data[4:6], data[6:8], data[8:10], data[10:])
		password, err := token(24)
		if err != nil {
			return nil, err
		}
		auth, err := token(24)
		if err != nil {
			return nil, err
		}
		sub, err := token(16)
		if err != nil {
			return nil, err
		}
		p.Clients = append(p.Clients, provisionedClient{Email: name, ID: id, Password: password, Auth: auth, SubID: sub, Flow: "xtls-rprx-vision", Enable: true})
	}
	return p, nil
}

func (p *provisioningIntent) inbounds(c config) ([]provisionedInbound, error) {
	if p.Version != 1 || p.ConfigHash != provisioningHash(c) || len(p.Clients) != len(c.Users) || p.InboundIDs == nil || p.AppliedClients == nil {
		return nil, errors.New("provisioning intent does not match frozen configuration")
	}
	private, err := base64.RawURLEncoding.DecodeString(p.PrivateKey)
	if err != nil {
		return nil, errors.New("invalid frozen Reality key")
	}
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, errors.New("invalid frozen Reality key")
	}
	short, err := hex.DecodeString(p.ShortID)
	if err != nil || len(short) != 8 {
		return nil, errors.New("invalid frozen Reality short ID")
	}
	seen := map[string]bool{}
	for i, client := range p.Clients {
		if client.Email != c.Users[i] || !client.Enable || client.Flow != "xtls-rprx-vision" || client.TotalGB != 0 || client.ExpiryTime != 0 || client.LimitIP != 0 {
			return nil, errors.New("frozen client configuration changed")
		}
		for _, value := range []string{client.ID, client.Password, client.Auth, client.SubID} {
			if value == "" || seen[value] {
				return nil, errors.New("invalid frozen client credentials")
			}
			seen[value] = true
		}
	}
	for email, done := range p.AppliedClients {
		if !done || !slices.Contains(c.Users, email) {
			return nil, errors.New("invalid client provisioning receipt")
		}
	}
	for name, id := range p.InboundIDs {
		if !slices.Contains([]string{"vless-reality", "trojan-httpupgrade", "hysteria2"}, name) || id <= 0 {
			return nil, errors.New("invalid inbound provisioning receipt")
		}
	}
	encode := func(value any) string { data, _ := json.Marshal(value); return string(data) }
	e := c.Env
	external := func(tls string) any {
		return []any{map[string]any{"forceTls": tls, "dest": e["PUBLIC_DOMAIN"], "port": 443, "remark": ""}}
	}
	public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	streams := []map[string]any{
		{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"show": false, "target": "traefik:8443", "xver": 2, "serverNames": []string{e["PUBLIC_DOMAIN"]}, "privateKey": p.PrivateKey, "shortIds": []string{p.ShortID}, "settings": map[string]any{"publicKey": public, "fingerprint": e["REALITY_FINGERPRINT"], "serverName": "", "spiderX": "/"}}, "tcpSettings": map[string]any{"acceptProxyProtocol": false, "header": map[string]string{"type": "none"}}, "externalProxy": external("same")},
		{"network": "httpupgrade", "security": "none", "httpupgradeSettings": map[string]any{"acceptProxyProtocol": false, "path": e["TROJAN_PATH"], "host": e["PUBLIC_DOMAIN"], "headers": map[string]string{}}, "externalProxy": external("tls")},
		{"network": "hysteria", "security": "tls", "tlsSettings": map[string]any{"serverName": e["PUBLIC_DOMAIN"], "alpn": []string{"h3"}, "certificates": []any{map[string]string{"certificateFile": "/certs/certs/" + e["CERT_BASENAME"] + ".crt", "keyFile": "/certs/private/" + e["CERT_BASENAME"] + ".key"}}}, "hysteriaSettings": map[string]any{"version": 2, "ignoreClientBandwidth": true}, "externalProxy": external("tls")},
	}
	settings := []any{map[string]any{"clients": []any{}, "decryption": "none", "encryption": "none", "fallbacks": []any{}}, map[string]any{"clients": []any{}, "fallbacks": []any{}}, map[string]any{"version": 2, "users": []any{}}}
	rows := []provisionedInbound{{Remark: "vless-reality", Port: 443, Protocol: "vless"}, {Remark: "trojan-httpupgrade", Port: 12812, Protocol: "trojan"}, {Remark: "hysteria2", Port: 443, Protocol: "hysteria"}}
	for i := range rows {
		rows[i].Enable = true
		rows[i].Settings = encode(settings[i])
		rows[i].StreamSettings = encode(streams[i])
		rows[i].Sniffing = encode(map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "metadataOnly": false, "routeOnly": true})
		rows[i].Allocate = encode(map[string]any{"strategy": "always", "refresh": 5, "concurrency": 3})
	}
	return rows, nil
}

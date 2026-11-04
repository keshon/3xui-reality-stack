package xui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// PasswordStack enables only the experimental password workflow. Default Stack
// remains read-only; command wiring must explicitly select this adapter.
type PasswordStack struct{ Stack }

func (s PasswordStack) ReviewState(ctx context.Context, root string) (deployment.ReviewSpec, error) {
	spec, err := s.Stack.ReviewState(ctx, root)
	if err != nil {
		return spec, err
	}
	binding, err := decodePasswordBinding(spec.Binding)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	panel, err := managedPanel(binding)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	panel.transport.CloseIdleConnections()
	return spec, nil
}

func (s PasswordStack) OpenReviewed(ctx context.Context, root, kind string, raw json.RawMessage) (deployment.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if kind != "xui-password-v1" {
		return nil, errors.New("unsupported password workflow")
	}
	binding, err := decodePasswordBinding(raw)
	if err != nil {
		return nil, err
	}
	panel, err := managedPanel(binding)
	if err != nil {
		return nil, err
	}
	return s.Stack.openPassword(root, panel, raw)
}

// managedPanel keeps the certificate hostname and HTTP Host header while
// dialing the reviewed local management listener. It never resolves public DNS
// or disables TLS verification. Credentials stay on the controller host.
func managedPanel(binding passwordBinding) (*Panel, error) {
	var applied baseline
	if json.Unmarshal(binding.Inputs[".stack/applied.json"], &applied) != nil || applied.Version != 1 || !valid(applied.Config) {
		return nil, errors.New("invalid reviewed management configuration")
	}
	return managedConfigurationPanel(applied.Config)
}

func managedConfigurationPanel(c config) (*Panel, error) {
	if !valid(c) {
		return nil, errors.New("invalid management configuration")
	}
	env := c.Env
	domain, prefix := env["PANEL_DOMAIN"], strings.TrimRight(env["PANEL_PATH"], "/")
	if !panelDomain(domain) || !panelPath(prefix) {
		return nil, errors.New("management domain or panel path is invalid")
	}
	port := env["MANAGEMENT_PORT"]
	if port == "" {
		port = "2053"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return nil, errors.New("invalid management port")
	}
	bind := env["MANAGEMENT_BIND"]
	if bind == "" || bind == "0.0.0.0" {
		bind = "127.0.0.1"
	}
	ip := net.ParseIP(bind)
	role := env["ROLE"]
	if ip == nil || ip.To4() == nil || !ip.IsLoopback() || (role != "" && role != "standalone" && role != "master") {
		return nil, errors.New("password execution requires local IPv4 management access on a standalone or master installation")
	}
	authority := net.JoinHostPort(domain, port)
	panel, err := NewPanel("https://" + authority + prefix)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	panel.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != authority {
			return nil, errors.New("unexpected management connection target")
		}
		return dialer.DialContext(ctx, "tcp", net.JoinHostPort(bind, port))
	}
	return panel, nil
}

func panelDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || !strings.Contains(domain, ".") || net.ParseIP(domain) != nil {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func panelPath(prefix string) bool {
	if !strings.HasPrefix(prefix, "/") || len(prefix) < 2 {
		return false
	}
	for _, part := range strings.Split(prefix[1:], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._~-", r) {
				return false
			}
		}
	}
	return true
}

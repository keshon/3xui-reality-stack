package xui

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/certificates"
)

func verifyWebCertificates(ctx context.Context, root string, inputs map[string][]byte) error {
	var c config
	if json.Unmarshal(inputs[".stack/config.json"], &c) != nil {
		return errors.New("invalid frozen certificate settings")
	}
	exporter, err := certificates.New(certificates.Options{Source: filepath.Join(root, acmeStateFile), Destination: filepath.Join(root, "shared/certs"), Resolver: "le", Domain: c.Env["CERT_BASENAME"]})
	if err != nil {
		return err
	}
	hosts := []string{c.Env["PUBLIC_DOMAIN"], c.Env["PANEL_DOMAIN"]}
	ready, err := exporter.Ready(ctx, hosts)
	if err != nil || !ready {
		return errors.New("web certificate export is not ready; retry after ACME issuance and export")
	}
	for _, name := range hosts {
		if err := probeDecoy(ctx, name, "127.0.0.1:2053", inputs["decoy/site/index.html"], nil); err != nil {
			return err
		}
	}
	return nil
}

// The production caller uses the system trust roots. The optional pool is used
// by local synthetic TLS tests; no controller or RPC input can select it.
func probeDecoy(ctx context.Context, domain, address string, expected []byte, roots *x509.CertPool) error {
	ip, port, err := net.SplitHostPort(address)
	parsed := net.ParseIP(ip)
	if err != nil || parsed == nil || !parsed.IsLoopback() || !initialDomain(domain) || len(expected) == 0 {
		return errors.New("invalid local web readiness target")
	}
	authority := net.JoinHostPort(domain, port)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{ServerName: domain, RootCAs: roots, MinVersion: tls.VersionTLS13}}
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if network != "tcp" || target != authority {
			return nil, errors.New("unexpected web readiness connection target")
		}
		return dialer.DialContext(ctx, "tcp", address)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+authority+"/", nil)
	if err != nil {
		return errors.New("cannot build web readiness request")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("verified local HTTPS readiness probe failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(len(expected))+1))
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(body, expected) {
		return errors.New("local HTTPS did not serve the reviewed decoy page")
	}
	return nil
}

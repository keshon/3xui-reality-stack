package xui

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func nativeSubscriptionLinks(t *testing.T, c config, p *provisioningIntent, client provisionedClient) []string {
	t.Helper()
	private, _ := base64.RawURLEncoding.DecodeString(p.PrivateKey)
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	makeLink := func(scheme, credential string, q url.Values) string {
		return (&url.URL{Scheme: scheme, Host: c.Env["PUBLIC_DOMAIN"] + ":443", User: url.User(credential), RawQuery: q.Encode(), Fragment: "lane"}).String()
	}
	return []string{
		makeLink("vless", client.ID, url.Values{"type": {"tcp"}, "security": {"reality"}, "encryption": {"none"}, "flow": {"xtls-rprx-vision"}, "pbk": {public}, "sid": {p.ShortID}, "sni": {c.Env["PUBLIC_DOMAIN"]}, "fp": {c.Env["REALITY_FINGERPRINT"]}}),
		makeLink("trojan", client.Password, url.Values{"type": {"httpupgrade"}, "security": {"tls"}, "host": {c.Env["PUBLIC_DOMAIN"]}, "path": {c.Env["TROJAN_PATH"]}}),
		makeLink("hysteria2", client.Auth, url.Values{"security": {"tls"}, "sni": {c.Env["PUBLIC_DOMAIN"]}, "alpn": {"h3"}}),
	}
}

func nativeSubscriptionBody(t *testing.T, c config, p *provisioningIntent, client provisionedClient) []byte {
	t.Helper()
	return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(nativeSubscriptionLinks(t, c, p, client), "\n"))))
}

func TestNativeSubscriptionChecksPublishedCredentialsAndAllThreeTransports(t *testing.T) {
	c := provisioningConfiguration(t)
	p, err := newProvisioningIntent(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := p.Clients[0]
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		body := []byte(encoding.EncodeToString([]byte(strings.Join(nativeSubscriptionLinks(t, c, p, client), "\r\n"))))
		if err := validateNativeSubscription(body, c, p, client); err != nil {
			t.Fatal(err)
		}
	}
	for _, fault := range []string{"missing", "duplicate", "credential", "host", "port", "path", "flow", "key", "insecure", "query-duplicate", "invalid-base64"} {
		t.Run(fault, func(t *testing.T) {
			links := nativeSubscriptionLinks(t, c, p, client)
			u, _ := url.Parse(links[0])
			q := u.Query()
			switch fault {
			case "missing":
				links = links[:2]
			case "duplicate":
				links = append(links, links[0])
			case "credential":
				u.User = url.User("wrong")
				links[0] = u.String()
			case "host":
				u.Host = c.Env["PANEL_DOMAIN"] + ":443"
				links[0] = u.String()
			case "port":
				u.Host = c.Env["PUBLIC_DOMAIN"] + ":2053"
				links[0] = u.String()
			case "path":
				trojan, _ := url.Parse(links[1])
				values := trojan.Query()
				values.Set("path", "/wrong")
				trojan.RawQuery = values.Encode()
				links[1] = trojan.String()
			case "flow":
				q.Set("flow", "")
				u.RawQuery = q.Encode()
				links[0] = u.String()
			case "key":
				q.Set("pbk", "wrong")
				u.RawQuery = q.Encode()
				links[0] = u.String()
			case "insecure":
				q.Set("allowInsecure", "1")
				u.RawQuery = q.Encode()
				links[0] = u.String()
			case "query-duplicate":
				q.Add("sni", "wrong")
				u.RawQuery = q.Encode()
				links[0] = u.String()
			}
			body := []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n"))))
			if fault == "invalid-base64" {
				body = []byte("invalid!")
			}
			if err := validateNativeSubscription(body, c, p, client); err == nil || strings.Contains(err.Error(), client.Password) || strings.Contains(err.Error(), client.SubID) {
				t.Fatal("invalid subscription accepted or credentials leaked")
			}
		})
	}
}

func TestSubscriptionReadUsesTLSWithoutAdminCookiesAndRefusesRedirects(t *testing.T) {
	for _, mode := range []string{"body", "redirect", "untrusted"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Method != "GET" {
					t.Error("subscription reader sent credentials")
				}
				if mode == "redirect" {
					http.Redirect(w, r, "https://example.test/private-id", http.StatusFound)
					return
				}
				_, _ = w.Write([]byte("body"))
			}))
			defer server.Close()
			p, err := NewPanel(server.URL + "/sub")
			if err != nil {
				t.Fatal(err)
			}
			defer p.transport.CloseIdleConnections()
			if mode != "untrusted" {
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				p.transport.TLSClientConfig = &tls.Config{RootCAs: roots}
			}
			body, err := readSubscriptionEndpoint(context.Background(), p, "private-id")
			if mode == "body" {
				if err != nil || string(body) != "body" {
					t.Fatal("trusted local subscription request failed")
				}
			} else if err == nil || strings.Contains(err.Error(), "private-id") {
				t.Fatal("unsafe request succeeded or leaked its URL")
			}
		})
	}
}

func TestSubscriptionVerificationRechecksPromotedResources(t *testing.T) {
	w, docker, _, dir := promotionWorkflowFixture(t)
	if err := runPromotion(t, w, dir); err != nil {
		t.Fatal(err)
	}
	var plan provisioningIntent
	if ok, err := readJSON(filepath.Join(dir, "provisioning.json"), &plan, true); err != nil || !ok {
		t.Fatal("missing frozen accounts")
	}
	c := docker.configuration
	reads := 0
	w.subscriptionRead = func(ctx context.Context, c config, subID string) ([]byte, error) {
		reads++
		for _, client := range plan.Clients {
			if client.SubID == subID {
				return nativeSubscriptionBody(t, c, &plan, client), nil
			}
		}
		t.Fatal("verification selected an unprovisioned subscription")
		return nil, nil
	}
	verify := func() error {
		unlock, err := deployment.Lock(w.root)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		snapshot, err := w.open(dir)
		if err != nil {
			t.Fatal(err)
		}
		return w.verifyNativeSubscriptions(t.Context(), dir, snapshot)
	}
	if err := verify(); err != nil || reads != len(c.Users) {
		t.Fatalf("subscription verification failed: %v", err)
	}
	docker.final.Container.StartedAt = "2026-11-04T10:05:00Z"
	docker.saveFinal()
	if err := verify(); err == nil || reads != len(c.Users) {
		t.Fatal("resource drift reached subscription requests")
	}
}

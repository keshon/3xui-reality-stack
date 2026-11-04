//go:build integration

package xui

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exercises the production bootstrap container recipe, metadata checks and API
// against the pinned image. No deployed stack, public port or systemd is used.
func TestPinnedPanelIsolation(t *testing.T) {
	if os.Getenv("STACK_TEST_CONTAINERS") != "1" {
		t.Skip("set STACK_TEST_CONTAINERS=1 on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("container integration requires a local Linux Docker daemon")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	input := validInitialInput()
	input.PublicDomain = "integration.example.test"
	c, _, err := initialConfiguration(rawInitialInput(t, input), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c.Env["PANEL_USER"], c.Env["PANEL_PASS"] = "integration-admin", "integration-private-test-password"
	c.Env["PANEL_PATH"], c.Env["SUB_PATH"], c.Env["JSON_PATH"] = "/integration-panel", "/integration-sub", "/integration-json"
	for _, name := range []string{"xui/db", "shared/certs"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeProvisioningTestCertificate(t, root, c)
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	owner := hex.EncodeToString(token[:])
	snapshot := installRuntimeSnapshot{Root: root, Owner: owner}
	w := &installRuntime{root: root}
	const format = `{"id":{{json .Id}},"digests":{{json .RepoDigests}}}`
	data, err := w.webDocker()(ctx, "image", "inspect", integrationPanelImage, "--format", format)
	if err != nil {
		t.Fatal("pull the pinned panel image explicitly before integration tests")
	}
	id, reference, err := resolvedWebImage(data, integrationPanelImage)
	if err != nil {
		t.Fatal(err)
	}
	intent := panelIntent{1, owner, id, reference}
	// Register cleanup before creation, including an interrupted create reply.
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		data, err := w.webDocker()(cleanupCtx, "inspect", panelContainerName(owner), "--format", `{"id":{{json .Id}},"owner":{{json (index .Config.Labels "io.stackctl.operation")}}}`)
		if err != nil {
			return
		}
		var receipt struct{ ID, Owner string }
		if json.Unmarshal(data, &receipt) != nil || !dockerID(receipt.ID) || receipt.Owner != owner {
			t.Error("refusing cleanup of an unowned bootstrap container")
			return
		}
		if _, err := w.webDocker()(cleanupCtx, "rm", "--force", "--volumes", receipt.ID); err != nil {
			t.Error("cannot clean up isolated bootstrap container")
		}
	})
	created, err := w.webDocker()(ctx, bootstrapCreateArgs(root, snapshot, intent)...)
	containerID := strings.TrimSpace(string(created))
	if err != nil || !dockerID(containerID) {
		t.Fatal("cannot create isolated bootstrap container")
	}
	if _, err := w.inspectBootstrapPanel(ctx, snapshot, intent, containerID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.webDocker()(ctx, "start", containerID); err != nil {
		t.Fatal("cannot start isolated bootstrap container")
	}
	current, err := w.inspectBootstrapPanel(ctx, snapshot, intent, containerID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newPanelBootstrap(c)
	if err != nil {
		t.Fatal(err)
	}
	open := func(prefix string) *Panel {
		p, err := NewPanel("http://" + net.JoinHostPort("127.0.0.1", bootstrapPort(current)) + prefix)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.transport.CloseIdleConnections)
		return p
	}
	wait := func(action func() error) {
		t.Helper()
		for {
			if action() == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("isolated panel did not become ready before the test deadline")
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	initial := open("")
	wait(func() error { return b.prepare(ctx, initial) })
	before := identityOfPanel(current)
	if _, err := w.webDocker()(ctx, "restart", containerID); err != nil {
		t.Fatal("cannot restart configured bootstrap panel")
	}
	current, err = w.inspectBootstrapPanel(ctx, snapshot, intent, containerID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != before.ID || current.StartedAt == before.StartedAt || current.Networks["bridge"].NetworkID != before.NetworkID {
		t.Fatal("restart did not preserve the isolated panel identity")
	}
	configured := open("/integration-panel")
	wait(func() error { return b.verify(ctx, configured) })
	session, err := configured.login(ctx, c.Env["PANEL_USER"], c.Env["PANEL_PASS"])
	if err != nil {
		t.Fatal(err)
	}
	identity := identityOfPanel(current)
	guard := func() error {
		observed, err := w.inspectBootstrapPanel(ctx, snapshot, intent, containerID)
		if err != nil {
			return err
		}
		if identityOfPanel(observed) != identity {
			return errors.New("isolated provisioning target changed")
		}
		return nil
	}
	operation := t.TempDir()
	if err := provisionFreshPanel(ctx, operation, c, session, guard); err != nil {
		t.Fatal(err)
	}
	preserved, err := session.preservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionFreshPanel(ctx, operation, c, session, guard); err != nil {
		t.Fatal(err)
	}
	after, err := session.preservation(ctx)
	if err != nil || string(preserved) != string(after) {
		t.Fatal("provisioning replay changed unified identities or subscriptions")
	}
	var plan provisioningIntent
	if ok, err := readJSON(filepath.Join(operation, "provisioning.json"), &plan, true); err != nil || !ok {
		t.Fatal("missing frozen integration identities")
	}
	address, err := w.webDocker()(ctx, "inspect", containerID, "--format", `{{(index .NetworkSettings.Networks "bridge").IPAddress}}`)
	if err != nil {
		t.Fatal("cannot inspect isolated subscription address")
	}
	ip := net.ParseIP(strings.TrimSpace(string(address)))
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		t.Fatal("isolated subscription address is not on the private bridge")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, account := range plan.Clients {
		req, err := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(ip.String(), "2096")+strings.TrimRight(c.Env["SUB_PATH"], "/")+"/"+url.PathEscape(account.SubID), nil)
		if err != nil {
			t.Fatal("cannot construct isolated subscription request")
		}
		req.Host = c.Env["PUBLIC_DOMAIN"]
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("isolated native subscription request failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			t.Fatal("isolated native subscription response failed")
		}
		if err := guard(); err != nil {
			t.Fatal(err)
		}
		if err := validateNativeSubscription(body, c, &plan, account); err != nil {
			t.Fatal(err)
		}
		jsonRequest, err := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(ip.String(), "2096")+strings.TrimRight(c.Env["JSON_PATH"], "/")+"/"+url.PathEscape(account.SubID), nil)
		if err != nil {
			t.Fatal("cannot construct isolated JSON subscription request")
		}
		jsonRequest.Host = c.Env["PUBLIC_DOMAIN"]
		response, err := client.Do(jsonRequest)
		if err != nil {
			t.Fatal("isolated JSON subscription request failed")
		}
		jsonBody, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			t.Fatal("isolated JSON subscription response failed")
		}
		if _, err := checkedJSONOutbounds(jsonBody, c, &plan, account); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyFreshRouting(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := validateBootstrapDatabase(filepath.Join(root, "xui/db")); err != nil {
		entries, _ := os.ReadDir(filepath.Join(root, "xui/db"))
		for _, entry := range entries {
			t.Logf("isolated database entry: %s (directory=%v)", entry.Name(), entry.IsDir())
		}
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "xui/db/x-ui.db")); err != nil || info.Size() == 0 {
		t.Fatal("bootstrap did not initialize its private database")
	}
}

// A test-only server certificate allows the pinned core to parse the Hysteria2
// inbound while every transport remains unpublished. It is not an ACME fixture.
func writeProvisioningTestCertificate(t *testing.T, root string, c config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{c.Env["PUBLIC_DOMAIN"]}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"certs", "private"} {
		if err := os.MkdirAll(filepath.Join(root, "shared/certs", sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "shared/certs/certs", c.Env["CERT_BASENAME"]+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shared/certs/private", c.Env["CERT_BASENAME"]+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
}

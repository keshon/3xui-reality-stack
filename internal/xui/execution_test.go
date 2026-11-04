package xui

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func endpointBinding(env map[string]string) passwordBinding {
	c := config{Schema: 1, Env: env, Users: []string{}, Nodes: []map[string]string{}}
	data, _ := json.Marshal(baseline{Version: 1, Config: c})
	return passwordBinding{Inputs: map[string][]byte{".stack/applied.json": data}}
}

func TestManagedPanelUsesLocalDialAndValidatesTLSHostname(t *testing.T) {
	hit := make(chan string, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- r.Host + r.URL.Path
		_, _ = io.WriteString(w, "management")
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	cert := server.Certificate()
	if len(cert.DNSNames) == 0 || !panelDomain(cert.DNSNames[0]) {
		t.Fatal("test certificate has no usable DNS name")
	}
	domain := cert.DNSNames[0]
	u, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	panel, err := managedPanel(endpointBinding(map[string]string{"PANEL_DOMAIN": domain, "PANEL_PATH": "/private-panel/", "MANAGEMENT_PORT": port}))
	if err != nil {
		t.Fatal(err)
	}
	defer panel.transport.CloseIdleConnections()
	client := &http.Client{Transport: panel.transport}
	// Local connection routing must not implicitly trust a private certificate.
	if response, err := client.Get(panel.base + "/health"); err == nil {
		response.Body.Close()
		t.Fatal("untrusted management certificate accepted")
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	panel.transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	response, err := client.Get(panel.base + "/health")
	if err != nil {
		t.Fatal("trusted management endpoint could not be reached through local dial")
	}
	response.Body.Close()
	if got := <-hit; got != net.JoinHostPort(domain, port)+"/private-panel/health" {
		t.Fatal("local dial changed the HTTP hostname or panel path")
	}
	panel.transport.CloseIdleConnections()
	panel.transport.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: "wrong.invalid", MinVersion: tls.VersionTLS12}
	if response, err := client.Get(panel.base + "/health"); err == nil {
		response.Body.Close()
		t.Fatal("management TLS hostname mismatch accepted")
	}
	if _, err := panel.transport.DialContext(context.Background(), "tcp", "unreviewed.invalid:443"); err == nil {
		t.Fatal("management dial accepted an unreviewed target")
	}
	if panel.transport.Proxy != nil {
		t.Fatal("management transport uses environment proxy settings")
	}
}

func TestManagedPanelRejectsUnsupportedEndpointsWithoutDialing(t *testing.T) {
	for key, values := range map[string][]string{
		"PANEL_DOMAIN":    {"", "127.0.0.1", "user@panel.test", "panel.test/other", "panel.test?private", "bad\n.test", "-panel.test", "panel..test"},
		"PANEL_PATH":      {"", "/", "/../panel", "/panel?private", "/panel#private", "/panel%2fsecret", "/panel//child"},
		"MANAGEMENT_PORT": {"0", "65536", "02053", "private"},
		"MANAGEMENT_BIND": {"192.0.2.1", "::1", "private"},
		"ROLE":            {"node", "unknown"},
	} {
		for _, value := range values {
			env := map[string]string{"PANEL_DOMAIN": "panel.test", "PANEL_PATH": "/private-panel"}
			env[key] = value
			if _, err := managedPanel(endpointBinding(env)); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsupported %s accepted or echoed", key)
			}
		}
	}
	for _, bind := range []string{"", "127.0.0.1", "127.0.0.2", "0.0.0.0"} {
		panel, err := managedPanel(endpointBinding(map[string]string{"PANEL_DOMAIN": "panel.test", "PANEL_PATH": "/private-panel", "MANAGEMENT_BIND": bind}))
		if err != nil {
			t.Fatal("supported local management binding rejected")
		}
		panel.transport.CloseIdleConnections()
	}
}

func TestPasswordExecutionCapabilityRequiresSeparateAdapter(t *testing.T) {
	root := passwordFixture(t)
	var desired config
	var previous baseline
	readJSON(root+"/.stack/config.json", &desired, true)
	readJSON(root+"/.stack/applied.json", &previous, true)
	desired.Env["PANEL_DOMAIN"], previous.Config.Env["PANEL_DOMAIN"] = "panel.test", "panel.test"
	desired.Env["PANEL_PATH"], previous.Config.Env["PANEL_PATH"] = "/private-panel", "/private-panel"
	writeJSON(t, root, "config.json", desired)
	writeJSON(t, root, "applied.json", previous)
	stack := passwordTestStack(root)
	if _, enabled := any(stack).(deployment.ExecutionAdapter); enabled {
		t.Fatal("default stack exposes execution")
	}
	executor := PasswordStack{Stack: stack}
	review, err := (deployment.Engine{Root: root}).Review(context.Background(), executor)
	if err != nil || !review.CanApply {
		t.Fatal("explicit password adapter cannot create an executable review")
	}
	spec, err := executor.ReviewState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	w, err := executor.OpenReviewed(context.Background(), root, spec.Kind, spec.Binding)
	if err != nil || w.Kind() != "xui-password-v1" || w.DeploymentRoot() != root {
		t.Fatal("password adapter could not open frozen workflow")
	}
	if _, err := executor.OpenReviewed(context.Background(), root, "unrelated-operation", spec.Binding); err == nil {
		t.Fatal("password adapter accepted an unrelated operation")
	}
}

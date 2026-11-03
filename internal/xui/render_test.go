package xui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	stackassets "github.com/keshon/3xui-reality-stack"
)

func renderedInitial(t *testing.T, input initialInput) (config, map[string][]byte) {
	t.Helper()
	c, _, err := initialConfiguration(rawInitialInput(t, input), bytes.NewReader(make([]byte, 128)))
	if err != nil {
		t.Fatal(err)
	}
	files, err := renderDeployment(c)
	if err != nil {
		t.Fatal(err)
	}
	return c, files
}

func readCompose(t *testing.T, data []byte) composeDocument {
	t.Helper()
	var doc composeDocument
	if json.Unmarshal(data, &doc) != nil {
		t.Fatal("invalid generated Compose JSON")
	}
	return doc
}

func TestRenderedTopologyKeepsSocketOwnershipAndRoutes(t *testing.T) {
	c, files := renderedInitial(t, validInitialInput())
	var policy map[string]any
	json.Unmarshal(stackassets.Compatibility(), &policy)
	xui := readCompose(t, files["xui/compose.json"])
	proxy := readCompose(t, files["traefik/compose.json"])
	site := readCompose(t, files["decoy/compose.json"])
	panel, traefik, decoy := xui.Services["3x-ui"], proxy.Services["traefik"], site.Services["decoy"]
	if panel.Image != policy["panel_image"] || traefik.Image != policy["traefik_image"] {
		t.Fatal("renderer ignored embedded image policy")
	}
	if !reflect.DeepEqual(panel.Ports, []string{"443:443/tcp", "443:443/udp"}) ||
		!reflect.DeepEqual(traefik.Ports, []string{"80:80/tcp", "127.0.0.1:2053:8443/tcp"}) || len(decoy.Ports) != 0 {
		t.Fatal("public ports or management exposure changed")
	}
	for name, doc := range map[string]composeDocument{"xui": xui, "traefik": proxy, "decoy": site} {
		if doc.Name != name || len(doc.Services) != 1 {
			t.Fatal("unexpected deployment project or service")
		}
		network := doc.Networks["proxy"].(map[string]any)
		if network["external"] != true || network["name"] != "proxy" {
			t.Fatal("shared network ownership changed")
		}
		for _, service := range doc.Services {
			if !reflect.DeepEqual(service.Networks, []string{"proxy"}) || service.Logging["driver"] != "json-file" {
				t.Fatal("missing network or bounded logging")
			}
		}
	}
	for _, command := range []string{"--entrypoints.websecure.address=:8443", "--entrypoints.websecure.proxyProtocol.trustedIPs=" + c.Env["PROXY_SUBNET"], "--providers.docker.exposedByDefault=false"} {
		if !slices.Contains(traefik.Command, command) {
			t.Fatal("fallback or provider configuration changed")
		}
	}
	for _, route := range []struct{ name, port, domain string }{
		{"xui-panel", "2053", c.Env["PANEL_DOMAIN"]}, {"xui-sub", "2096", c.Env["PUBLIC_DOMAIN"]}, {"trojan-hu", "12812", c.Env["PUBLIC_DOMAIN"]},
	} {
		prefix := "traefik.http.routers." + route.name
		target := panel.Labels[prefix+".service"]
		if target == "" || panel.Labels["traefik.http.services."+target+".loadbalancer.server.port"] != route.port ||
			!strings.Contains(panel.Labels[prefix+".rule"], "Host(`"+route.domain+"`)") || panel.Labels[prefix+".entrypoints"] != "websecure" {
			t.Fatal("panel, subscription or tunnel route broken")
		}
	}
	if decoy.Labels["traefik.http.routers.decoy.priority"] != "1" || decoy.Labels["traefik.http.routers.decoy-catchall.rule"] != "PathPrefix(`/`)" || panel.Labels["traefik.http.routers.trojan-hu.priority"] != "100" {
		t.Fatal("route precedence changed")
	}
	for _, name := range renderedFiles {
		if len(files[name]) == 0 {
			t.Fatal("missing bundled deployment asset")
		}
	}
	for name, data := range files {
		if strings.HasSuffix(name, "compose.json") && (bytes.Contains(data, []byte(c.Env["PANEL_PASS"])) || bytes.Contains(data, []byte("python:"))) {
			t.Fatal("Compose includes panel password or Python sidecar")
		}
	}
	var tls struct {
		TLS struct {
			Stores map[string]struct {
				DefaultCertificate map[string]string `json:"defaultCertificate"`
			} `json:"stores"`
		} `json:"tls"`
	}
	if json.Unmarshal(files["traefik/dynamic/tls.json"], &tls) != nil || tls.TLS.Stores["default"].DefaultCertificate["certFile"] != "/certs/certs/www.example.test.crt" {
		t.Fatal("certificate mount and default certificate path differ")
	}
}

func TestRenderedACMEModesAndComposeLiterals(t *testing.T) {
	for _, mode := range []string{"http", "dns"} {
		t.Run(mode, func(t *testing.T) {
			input := validInitialInput()
			input.ACMEMode = mode
			if mode == "dns" {
				input.DNSToken = `secret${CF_DNS_API_TOKEN}$with"quote\slash`
			}
			_, files := renderedInitial(t, input)
			service := readCompose(t, files["traefik/compose.json"]).Services["traefik"]
			commands := strings.Join(service.Command, "\n")
			if strings.Contains(commands, ".httpchallenge=true") != (mode == "http") || strings.Contains(commands, ".dnschallenge=true") != (mode == "dns") {
				t.Fatal("ACME modes were mixed")
			}
			if mode == "dns" {
				if service.Environment["CF_DNS_API_TOKEN"] != strings.ReplaceAll(input.DNSToken, "$", "$$") || !strings.Contains(commands, ".sans=*.example.test") {
					t.Fatal("DNS secret literal or wildcard changed")
				}
			} else if _, exists := service.Environment["CF_DNS_API_TOKEN"]; exists {
				t.Fatal("HTTP mode includes DNS credential")
			}
			if mode == "http" && (!strings.Contains(commands, ".main=www.example.test") || !strings.Contains(commands, ".sans=ops.example.test")) {
				t.Fatal("HTTP bootstrap certificates depend on starting the panel")
			}
		})
	}
}

func TestRendererRejectsUnsupportedConfigurationAndIsDeterministic(t *testing.T) {
	c, first := renderedInitial(t, validInitialInput())
	second, err := renderDeployment(c)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("rendering is not deterministic")
	}
	for key, value := range map[string]string{"ROLE": "node", "LANES": "amneziawg", "MANAGEMENT_BIND": "0.0.0.0", "MANAGEMENT_PORT": "443", "PROXY_SUBNET": "bad", "PUBLIC_DOMAIN": "../secret", "PANEL_PATH": "/`bad", "CERT_BASENAME": "../../private", "ACME_MODE": "invalid"} {
		original := c.Env[key]
		c.Env[key] = value
		if _, err := renderDeployment(c); err == nil {
			t.Fatalf("accepted unsupported %s", key)
		}
		c.Env[key] = original
	}
}

func TestGeneratedFileDriftPreventsInitializationRerun(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	raw := rawInitialInput(t, validInitialInput())
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "xui/compose.json")
	if err := os.WriteFile(path, []byte("existing-deployment-do-not-replace"), 0600); err != nil {
		t.Fatal(err)
	}
	before := files(t, root)
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err == nil {
		t.Fatal("overwrote modified deployment")
	}
	if !reflect.DeepEqual(before, files(t, root)) {
		t.Fatal("failed rerun modified existing files")
	}
}

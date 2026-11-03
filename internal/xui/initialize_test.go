package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func validInitialInput() initialInput {
	return initialInput{Schema: 1, BaseDomain: "example.test", PublicDomain: "www.example.test", PanelDomain: "ops.example.test", Email: "admin@example.test", Users: []string{"alice", "bob"}}
}

func rawInitialInput(t *testing.T, input initialInput) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInitializePublishesOnlyPrivateDraftAndRerunsPreserveSecrets(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "new-stack")
	raw := rawInitialInput(t, validInitialInput())
	stack := Stack{docker: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("initialization invoked Docker")
		return nil, nil
	}}
	report, err := stack.Initialize(context.Background(), root, raw)
	if err != nil || report.Status != "prepared" || report.CanApply {
		t.Fatal("initialization failed or claimed an applied stack")
	}
	var c config
	if ok, err := readJSON(filepath.Join(root, ".stack/config.json"), &c, true); err != nil || !ok || !valid(c) {
		t.Fatal("initial configuration schema invalid")
	}
	if c.Env["PANEL_PASS"] == "" || c.Env["PANEL_USER"] == "" || c.Env["RU_POLICY"] != "allow" || c.Env["MANAGEMENT_BIND"] != "127.0.0.1" || c.Env["LANES"] != "vless-reality,trojan-httpupgrade,hysteria2" {
		t.Fatal("initial configuration lacks safe credentials or intended defaults")
	}
	for _, key := range []string{"PANEL_PATH", "SUB_PATH", "JSON_PATH"} {
		if !panelPath(c.Env[key]) {
			t.Fatal("private path was not generated")
		}
	}
	public, _ := json.Marshal(report)
	for _, value := range []string{c.Env["PANEL_PASS"], c.Env["PANEL_PATH"], "alice", root} {
		if bytes.Contains(public, []byte(value)) {
			t.Fatal("initialization report exposed private inputs")
		}
	}
	before := files(t, root)
	if _, err := stack.Initialize(context.Background(), root, raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, files(t, root)) {
		t.Fatal("rerun regenerated credentials, paths or configuration")
	}
	if _, err := os.Stat(filepath.Join(root, ".stack/applied.json")); !os.IsNotExist(err) {
		t.Fatal("initialization created an applied baseline")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 || entries[0].Name() != "new-stack" {
		t.Fatal("initialization left staging state after publication")
	}
}

func TestInitializeRejectsChangedOrExistingState(t *testing.T) {
	for _, scenario := range []string{"empty-existing", "legacy", "managed", "changed-file", "changed-input", "applied", "held-lock"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "target")
			raw := rawInitialInput(t, validInitialInput())
			if scenario == "empty-existing" || scenario == "legacy" || scenario == "managed" {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				if scenario == "legacy" {
					os.WriteFile(filepath.Join(root, ".env"), []byte("private-existing"), 0600)
				}
				if scenario == "managed" {
					os.Mkdir(filepath.Join(root, ".stack"), 0700)
					os.WriteFile(filepath.Join(root, ".stack/config.json"), []byte("private-existing"), 0600)
				}
			} else {
				if _, err := (Stack{}).Initialize(context.Background(), root, raw); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "changed-file":
					os.WriteFile(filepath.Join(root, ".env"), []byte("private-manual-change"), 0600)
				case "changed-input":
					input := validInitialInput()
					input.PanelPassword = "private-requested-change"
					raw = rawInitialInput(t, input)
				case "applied":
					os.WriteFile(filepath.Join(root, ".stack/applied.json"), []byte(`{"version":1}`), 0600)
				case "held-lock":
					// Acquire after the file snapshot; Windows also denies reads
					// of the locked byte, including the test's own inventory read.
				}
			}
			before := files(t, root)
			if scenario == "held-lock" {
				unlock, err := deployment.Lock(root)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			if _, err := (Stack{}).Initialize(context.Background(), root, raw); err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("initialization accepted or echoed existing-state conflicts")
			}
			for path, data := range before {
				actual, _ := os.ReadFile(path)
				if string(actual) != data {
					t.Fatal("rejected initialization changed existing files")
				}
			}
		})
	}
}

func TestInitializationValidationAndLiterals(t *testing.T) {
	for _, scenario := range []string{"schema", "domain", "numeric-tld", "same-host", "email", "email-without-domain-dot", "empty-users", "duplicate-users", "account", "dns-token", "wildcard", "http-token", "unknown-mode", "control", "oversized-password", "default-password"} {
		t.Run(scenario, func(t *testing.T) {
			input := validInitialInput()
			switch scenario {
			case "schema":
				input.Schema = 2
			case "domain":
				input.PublicDomain = "https://private-value.test"
			case "numeric-tld":
				input.PublicDomain = "www.123"
			case "same-host":
				input.PublicDomain = input.PanelDomain
			case "email":
				input.Email = "private-invalid-email"
			case "email-without-domain-dot":
				input.Email = "admin.name@example"
			case "empty-users":
				input.Users = nil
			case "duplicate-users":
				input.Users = []string{"alice", "alice"}
			case "account":
				input.Users = []string{"private-invalid-name"}
			case "dns-token":
				input.ACMEMode = "dns"
			case "wildcard":
				input.ACMEMode = "dns"
				input.DNSToken = "private-token"
				input.PublicDomain = "nested.www.example.test"
			case "http-token":
				input.DNSToken = "private-token"
			case "unknown-mode":
				input.ACMEMode = "private-invalid-mode"
			case "control":
				input.PanelPassword = "private\npassword"
			case "oversized-password":
				input.PanelPassword = strings.Repeat("x", 4097)
			case "default-password":
				input.PanelPassword = "admin"
			}
			root := filepath.Join(t.TempDir(), "target")
			if _, err := (Stack{}).Initialize(context.Background(), root, rawInitialInput(t, input)); err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("invalid initialization accepted or secret echoed")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("invalid input created a target")
			}
		})
	}
	input := validInitialInput()
	input.PanelPassword = newPassword
	input.PanelUser = "test-admin"
	input.ACMEMode = "dns"
	input.DNSToken = "private-token"
	input.PublicDomain = "WWW.EXAMPLE.TEST."
	c, _, err := initialConfiguration(rawInitialInput(t, input), bytes.NewReader(make([]byte, 128)))
	if err != nil || c.Env["PANEL_PASS"] != newPassword || c.Env["PUBLIC_DOMAIN"] != "www.example.test" || c.Env["CERT_BASENAME"] != "example.test" {
		t.Fatal("valid DNS configuration or literal password was changed")
	}
	artifacts, err := initialArtifacts(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(artifacts[".env"]), "\n") {
		if strings.HasPrefix(line, "PANEL_PASS=") {
			value, err := parseLiteral(strings.TrimPrefix(line, "PANEL_PASS="))
			if err != nil || value != newPassword {
				t.Fatal("rendered password literal changed")
			}
		}
	}
}

func TestInitializeRemoteUsesFixedRootAndReturnsNoCredentials(t *testing.T) {
	root := filepath.Join(t.TempDir(), "remote-target")
	controller := control.Controller{Root: root, Stack: Stack{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	left, right := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer right.Close(); _ = control.Serve(ctx, right, right, controller) }()
	client, err := control.Connect(ctx, left)
	if err != nil {
		left.Close()
		t.Fatal(err)
	}
	defer func() { client.Close(); <-done }()
	input := validInitialInput()
	input.PanelPassword = newPassword
	report, err := client.Initialize(ctx, rawInitialInput(t, input))
	if err != nil || report.CanApply {
		t.Fatal("remote initialization failed or enabled deployment")
	}
	data, _ := json.Marshal(report)
	if bytes.Contains(data, []byte("пароль")) {
		t.Fatal("private password returned by remote initialization")
	}
	if _, err := os.Stat(filepath.Join(root, ".stack/config.json")); err != nil {
		t.Fatal("remote initialization did not use fixed controller root")
	}
}

func TestInitializedConfigMatchesLegacyParser(t *testing.T) {
	interpreter := os.Getenv("STACK_TEST_PYTHON")
	if interpreter == "" {
		for _, name := range []string{"python3", "python"} {
			if path, err := exec.LookPath(name); err == nil {
				interpreter = path
				break
			}
		}
	}
	if interpreter == "" {
		t.Skip("Python unavailable for configuration interoperability check")
	}
	input := validInitialInput()
	input.PanelPassword = newPassword
	c, _, err := initialConfiguration(rawInitialInput(t, input), bytes.NewReader(make([]byte, 128)))
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := initialArtifacts(c)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"config": c, "env": string(artifacts[".env"])})
	cmd := exec.Command(interpreter, "-c", `import json,sys; from stackctl.config import normalize; from stackctl.envio import parse_env; p=json.load(sys.stdin); c=normalize(p["config"]); assert c==p["config"]; assert parse_env(p["env"])==c["env"]`)
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1")
	cmd.Stdin = bytes.NewReader(payload)
	if err := cmd.Run(); err != nil {
		t.Fatal("Go initialized configuration differs from the current schema or literal parser")
	}
}

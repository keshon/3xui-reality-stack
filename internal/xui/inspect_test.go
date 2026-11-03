package xui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const secret = "private-test-password"

func writeJSON(t *testing.T, root, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".stack", name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (string, config, baseline) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".stack"), 0700); err != nil {
		t.Fatal(err)
	}
	c := config{Schema: 1, Env: map[string]string{"PANEL_USER": "private-admin", "PANEL_PASS": secret, "AWG_PORT": "28844"}, Users: []string{"private-user"}, Nodes: []map[string]string{}}
	b := baseline{Version: 1, Config: c, Hashes: map[string]*string{}}
	for _, name := range []string{".env", "users.txt", "nodes.txt"} {
		data := []byte("private-generated-data-" + name)
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		b.Hashes[name] = &hash
	}
	writeJSON(t, root, "config.json", c)
	writeJSON(t, root, "applied.json", b)
	return root, c, b
}

func files(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPlanScopesAndReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		change       func(*config)
	}{
		{"unchanged", "unchanged", func(c *config) {}},
		{"password", "proposed", func(c *config) { c.Env["PANEL_PASS"] = "new-" + secret }},
		{"traffic node", "blocked", func(c *config) { c.Env["PANEL_PASS"] = "new-" + secret; c.Env["ROLE"] = "node" }},
		{"empty password", "blocked", func(c *config) { c.Env["PANEL_PASS"] = "" }},
		{"port", "blocked", func(c *config) { c.Env["AWG_PORT"] = "28900" }},
		{"password and port", "blocked", func(c *config) { c.Env["PANEL_PASS"] = "new-" + secret; c.Env["AWG_PORT"] = "28900" }},
		{"password and user", "blocked", func(c *config) { c.Env["PANEL_PASS"] = "new-" + secret; c.Users = append(c.Users, "another") }},
		{"password and node", "blocked", func(c *config) {
			c.Env["PANEL_PASS"] = "new-" + secret
			c.Nodes = append(c.Nodes, map[string]string{"token": secret})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, c, _ := fixture(t)
			tc.change(&c)
			writeJSON(t, root, "config.json", c)
			before := files(t, root)
			i, err := Inspect(root)
			if err != nil {
				t.Fatal(err)
			}
			p := i.Plan()
			if p.Status != tc.status {
				t.Fatalf("status = %s, want %s", p.Status, tc.status)
			}
			if !p.ReadOnly {
				t.Fatal("plan must be read-only")
			}
			if p.Status == "proposed" {
				var ids []string
				for _, a := range p.Actions {
					ids = append(ids, a.ID)
				}
				if !reflect.DeepEqual(ids, []string{"snapshot", "panel-password", "verify", "commit"}) {
					t.Fatalf("unexpected scope: %v", ids)
				}
			} else if len(p.Actions) != 0 {
				t.Fatal("blocked/unchanged plan has actions")
			}
			data, err := json.Marshal([]any{i.Report, p})
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{secret, "private-admin", "private-user", "private-generated"} {
				if strings.Contains(string(data), value) {
					t.Fatal("public output contains private state")
				}
			}
			if !reflect.DeepEqual(before, files(t, root)) {
				t.Fatal("inspection/plan changed deployment files")
			}
		})
	}
}

func TestDriftAndIncompleteStateBlockPlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string, baseline)
	}{
		{"output drift", func(t *testing.T, r string, b baseline) {
			if err := os.WriteFile(filepath.Join(r, ".env"), []byte(secret), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing baseline", func(t *testing.T, r string, b baseline) {
			if err := os.Remove(filepath.Join(r, ".stack", "applied.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown outputs", func(t *testing.T, r string, b baseline) {
			b.Hashes["unowned"] = nil
			writeJSON(t, r, "applied.json", b)
		}},
		{"unfinished apply", func(t *testing.T, r string, b baseline) {
			writeJSON(t, r, "operation.json", map[string]string{"status": "applying"})
		}},
		{"unknown journal", func(t *testing.T, r string, b baseline) {
			writeJSON(t, r, "operation.json", map[string]string{"status": secret})
		}},
		{"unfinished adoption", func(t *testing.T, r string, b baseline) {
			writeJSON(t, r, "adoption.json", map[string]string{"status": "preparing"})
		}},
		{"failed rollback", func(t *testing.T, r string, b baseline) {
			writeJSON(t, r, "upgrade.json", map[string]string{"status": "rollback_failed"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, b := fixture(t)
			tc.change(t, root, b)
			i, err := Inspect(root)
			if err != nil {
				t.Fatal(err)
			}
			p := i.Plan()
			if p.Status != "blocked" || len(p.Reasons) == 0 || len(p.Actions) != 0 {
				t.Fatalf("unsafe plan: %+v", p)
			}
			data, _ := json.Marshal(p)
			if strings.Contains(string(data), secret) {
				t.Fatal("journal status leaked")
			}
		})
	}
}

func TestCompletedLegacyJournals(t *testing.T) {
	root, _, _ := fixture(t)
	for _, name := range []string{"operation.json", "adoption.json"} {
		writeJSON(t, root, name, map[string]string{"status": "complete"})
	}
	writeJSON(t, root, "upgrade.json", map[string]string{"status": "rolled_back"})
	i, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if i.Plan().Status != "unchanged" {
		t.Fatal("terminal legacy journals blocked inspection")
	}
}

func TestMalformedStateDoesNotEchoSecrets(t *testing.T) {
	for _, body := range []string{`{"schema_version":"` + secret + `"}`, `{"` + secret + `":true}`, `null`, `{}`, `{} {}`} {
		t.Run(body, func(t *testing.T) {
			root, _, _ := fixture(t)
			if err := os.WriteFile(filepath.Join(root, ".stack", "config.json"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Inspect(root)
			if err == nil {
				t.Fatal("malformed state accepted")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("error echoed private data")
			}
		})
	}
}

func TestMissingStateDoesNotCreateDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	i, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if i.Report.Managed || i.Plan().Status != "blocked" {
		t.Fatal("missing deployment accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("inspection created directory")
	}
}

func TestUnknownAppliedConfigurationAndNullEnvironmentRejected(t *testing.T) {
	root, c, _ := fixture(t)
	c.Env["PANEL_PASS"] = "new-password"
	writeJSON(t, root, "config.json", c)
	path := filepath.Join(root, ".stack", "applied.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var applied map[string]any
	if json.Unmarshal(data, &applied) != nil {
		t.Fatal("bad fixture")
	}
	applied["config"].(map[string]any)["future_setting"] = "private"
	writeJSON(t, root, "applied.json", applied)
	if _, err := Inspect(root); err == nil {
		t.Fatal("unknown applied configuration silently discarded")
	}
	var desired map[string]any
	data, _ = json.Marshal(c)
	if json.Unmarshal(data, &desired) != nil {
		t.Fatal("bad fixture")
	}
	desired["env"].(map[string]any)["PANEL_PASS"] = nil
	writeJSON(t, root, "config.json", desired)
	if _, err := Inspect(root); err == nil {
		t.Fatal("null environment entry accepted")
	}
}

package xui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

const newPassword = `new $HOME '"\ пароль & < >`

// fakePanel implements only the pinned API contract used by password changes.
// Unrecognized endpoints fail, so broad provisioning cannot pass this fixture.
type fakePanel struct {
	mu                                                            sync.Mutex
	password                                                      string
	updates                                                       int
	sessions                                                      map[string]bool
	twoFactor, badBackup, dropReply, blockNewLogin, alterIdentity bool
	identityChanged                                               bool
	paths                                                         []string
	server                                                        *httptest.Server
	onSettings                                                    func()
}

func newFakePanel(t *testing.T) (*fakePanel, *Panel) {
	t.Helper()
	f := &fakePanel{password: secret, sessions: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	p, err := NewPanel(f.server.URL + "/private-panel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.transport.CloseIdleConnections(); f.server.Close() })
	return f, p
}

func (f *fakePanel) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/private-panel")
	f.paths = append(f.paths, path)
	reply := func(ok bool, obj any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "obj": obj, "msg": "private-response-secret"})
	}
	if path == "/csrf-token" && r.Method == "GET" {
		http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "bound", Path: "/"})
		reply(true, "csrf-token")
		return
	}
	if r.Method == "POST" {
		cookie, err := r.Cookie("csrf")
		if err != nil || cookie.Value != "bound" || r.Header.Get("X-CSRF-TOKEN") != "csrf-token" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Origin") != f.server.URL || r.Header.Get("Referer") != f.server.URL+"/private-panel/" {
			http.Error(w, "private-csrf", 403)
			return
		}
	}
	if path == "/getTwoFactorEnable" && r.Method == "POST" {
		reply(true, f.twoFactor)
		return
	}
	if path == "/login" && r.Method == "POST" {
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad", 400)
			return
		}
		if f.blockNewLogin && body["password"] == newPassword {
			http.Error(w, "private-unavailable", 503)
			return
		}
		ok := body["username"] == "private-admin" && body["password"] == f.password
		if ok {
			sid := fmt.Sprintf("session-%d", len(f.sessions))
			f.sessions[sid] = true
			http.SetCookie(w, &http.Cookie{Name: "session", Value: sid, Path: "/"})
		}
		reply(ok, nil)
		return
	}
	cookie, err := r.Cookie("session")
	if err != nil || !f.sessions[cookie.Value] {
		http.Error(w, "private-no-auth", 401)
		return
	}
	switch {
	case path == "/panel/api/clients/export" && r.Method == "GET":
		subID := "original-subscription"
		if f.identityChanged {
			subID = "unexpected-replacement"
		}
		reply(true, []any{map[string]any{"client": map[string]string{"email": "alice", "id": "original-uuid", "password": "original-trojan", "subId": subID}, "inboundIds": []int{1, 2}}})
	case path == "/panel/api/setting/all" && r.Method == "POST":
		if f.onSettings != nil {
			f.onSettings()
		}
		reply(true, map[string]string{"webBasePath": "/private-panel/", "subPath": "/subscription/", "subJsonPath": "/json/", "subURI": "https://example.test/subscription/"})
	case path == "/panel/api/server/getDb" && r.Method == "GET":
		if f.badBackup {
			reply(false, nil)
			return
		}
		_, _ = w.Write(append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0}, 100)...))
	case path == "/panel/api/setting/updateUser" && r.Method == "POST":
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["oldUsername"] != "private-admin" || body["newUsername"] != "private-admin" || body["oldPassword"] != f.password || body["newPassword"] != newPassword {
			reply(false, nil)
			return
		}
		f.password = body["newPassword"]
		f.updates++
		if f.alterIdentity {
			f.identityChanged = true
		}
		if f.dropReply {
			f.dropReply = false
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		reply(true, nil)
	default:
		http.Error(w, "unexpected API", 404)
	}
}

func passwordFixture(t *testing.T) string {
	t.Helper()
	root, c, b := fixture(t)
	env := []byte("# preserve this\nPANEL_PASS=\"" + secret + "\"\nOTHER=unchanged\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), env, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(env)
	hash := hex.EncodeToString(sum[:])
	b.Hashes[".env"] = &hash
	writeJSON(t, root, "applied.json", b)
	c.Env["PANEL_PASS"] = newPassword
	writeJSON(t, root, "config.json", c)
	if err := os.MkdirAll(filepath.Join(root, "xui", "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := deployment.WriteJSON(filepath.Join(root, "compatibility.json"), map[string]any{"schema": 1, "panel_image": "ghcr.io/mhsanaei/3x-ui:v3.7.0", "traefik_image": "traefik:v3.7.12"}); err != nil {
		t.Fatal(err)
	}
	return root
}

func passwordTestStack(root string) Stack {
	return Stack{docker: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "exec" {
			return []byte("v3.7.0"), nil
		}
		image, id := "ghcr.io/mhsanaei/3x-ui:v3.7.0", strings.Repeat("a", 64)
		if args[len(args)-1] == "traefik" {
			image, id = "traefik:v3.7.12", strings.Repeat("b", 64)
		}
		return json.Marshal(map[string]any{
			"id": id, "image_id": "sha256:" + strings.Repeat("c", 64), "image": image, "running": true, "started_at": "2026-01-01T00:00:00Z",
			"mounts": []any{map[string]any{"Type": "bind", "Source": filepath.Join(root, "xui", "db"), "Destination": "/etc/x-ui", "RW": true}},
			"ports":  map[string]any{"8443/tcp": []any{map[string]string{"HostIp": "127.0.0.1", "HostPort": "2053"}}},
		})
	}}
}

func reviewPasswordFixture(t *testing.T, root string, panel *Panel) (*PasswordChange, error) {
	t.Helper()
	return passwordTestStack(root).reviewPassword(context.Background(), root, panel)
}

func resumePasswordFixture(t *testing.T, root string, panel *Panel) *PasswordChange {
	t.Helper()
	w := ResumePassword(root, panel)
	w.stack = passwordTestStack(root)
	return w
}

func operationDirectory(t *testing.T, root string) string {
	t.Helper()
	var marker struct {
		ID string `json:"id"`
	}
	data, err := os.ReadFile(filepath.Join(root, ".stack", "go-operation.json"))
	if err != nil || json.Unmarshal(data, &marker) != nil {
		t.Fatal("cannot read operation")
	}
	return filepath.Join(root, ".stack", "go-operations", marker.ID)
}

func TestPasswordChangeAndLostReply(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			root := passwordFixture(t)
			before := files(t, root)
			fake, panel := newFakePanel(t)
			fake.dropReply = lost
			w, err := reviewPasswordFixture(t, root, panel)
			if err != nil {
				t.Fatal(err)
			}
			if err := (deployment.Engine{Root: root}).Start(context.Background(), w); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			if fake.updates != 1 || fake.password != newPassword {
				t.Error("password was not changed exactly once")
			}
			fake.mu.Unlock()
			i, err := Inspect(root)
			if err != nil || i.Plan().Status != "unchanged" {
				t.Fatalf("baseline not committed: %v", err)
			}
			for _, name := range []string{"users.txt", "nodes.txt", ".stack/config.json"} {
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(data) != before[filepath.Join(root, name)] {
					t.Errorf("unrelated input changed: %s", name)
				}
			}
			data, err := os.ReadFile(filepath.Join(root, ".env"))
			if err != nil || !bytes.HasSuffix(data, []byte("OTHER=unchanged\n")) {
				t.Fatal("environment lines changed")
			}
			if _, err := os.Stat(filepath.Join(operationDirectory(t, root), "panel.db")); err != nil {
				t.Fatal("recovery database missing")
			}
		})
	}
}

func passwordFixtureWithoutNodesFile(t *testing.T) string {
	t.Helper()
	root := passwordFixture(t)
	if err := os.Remove(filepath.Join(root, "nodes.txt")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".stack/applied.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var previous map[string]any
	if json.Unmarshal(data, &previous) != nil {
		t.Fatal("invalid fixture baseline")
	}
	previous["output_hashes"].(map[string]any)["nodes.txt"] = nil
	if err := deployment.WriteJSON(path, previous); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPasswordChangePreservesAbsentNodesFile(t *testing.T) {
	root := passwordFixtureWithoutNodesFile(t)
	fake, panel := newFakePanel(t)
	fake.dropReply = true
	w, err := reviewPasswordFixture(t, root, panel)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the serialized binding as well as the in-memory review.
	encoded, err := json.Marshal(w.binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePasswordBinding(encoded); err != nil {
		t.Fatal(err)
	}
	if err := (deployment.Engine{Root: root}).Start(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "nodes.txt")); !os.IsNotExist(err) {
		t.Fatal("password operation generated an unrelated node file")
	}
	i, err := Inspect(root)
	if err != nil || i.Plan().Status != "unchanged" {
		t.Fatal("absent-file baseline was not preserved")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.updates != 1 {
		t.Fatal("ambiguous reply repeated the password write")
	}
}

func TestPasswordChangeRejectsNodesFileAppearingDuringOperation(t *testing.T) {
	for _, afterPreparation := range []bool{false, true} {
		t.Run(fmt.Sprint(afterPreparation), func(t *testing.T) {
			root := passwordFixtureWithoutNodesFile(t)
			fake, panel := newFakePanel(t)
			w, err := reviewPasswordFixture(t, root, panel)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if afterPreparation {
				if err := w.Prepare(t.Context(), dir); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "nodes.txt"), []byte{}, 0600); err != nil {
				t.Fatal(err)
			}
			if afterPreparation {
				var steps []deployment.Step
				steps, err = w.Steps(t.Context(), dir)
				if err == nil {
					err = steps[0].Run(t.Context())
				}
			} else {
				err = w.Prepare(t.Context(), dir)
			}
			if err == nil {
				t.Fatal("absent and empty node files were treated as identical")
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fake.updates != 0 {
				t.Fatal("file presence drift reached the password write")
			}
		})
	}
}

func TestPasswordResumeAfterRemoteCommitAndVerificationFailure(t *testing.T) {
	root := passwordFixture(t)
	before := files(t, root)
	fake, panel := newFakePanel(t)
	fake.blockNewLogin = true
	w, err := reviewPasswordFixture(t, root, panel)
	if err != nil {
		t.Fatal(err)
	}
	e := deployment.Engine{Root: root}
	err = e.Start(context.Background(), w)
	if err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("expected sanitized recoverable error")
	}
	for _, name := range []string{".env", ".stack/applied.json"} {
		data, _ := os.ReadFile(filepath.Join(root, name))
		if string(data) != before[filepath.Join(root, name)] {
			t.Fatal("local state committed before verification")
		}
	}
	fake.mu.Lock()
	fake.blockNewLogin = false
	fake.mu.Unlock()
	if err := e.Resume(context.Background(), resumePasswordFixture(t, root, panel)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.updates != 1 {
		t.Fatal("resume repeated a committed password mutation")
	}
}

func TestPartialLocalCommitReplaysWithFreshVerification(t *testing.T) {
	root := passwordFixture(t)
	fake, panel := newFakePanel(t)
	fake.blockNewLogin = true
	w, _ := reviewPasswordFixture(t, root, panel)
	e := deployment.Engine{Root: root}
	if e.Start(context.Background(), w) == nil {
		t.Fatal("expected verification failure")
	}
	dir := operationDirectory(t, root)
	snapshot, err := w.open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Model a process killed after .env rename, before baseline/checkpoint.
	if err := deployment.WriteFile(filepath.Join(root, ".env"), snapshot.Targets[".env"]); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".stack", "go-operation.json")
	var marker map[string]any
	data, _ := os.ReadFile(path)
	if json.Unmarshal(data, &marker) != nil {
		t.Fatal("bad marker")
	}
	marker["next"], marker["status"] = 2, "running"
	if err := deployment.WriteJSON(path, marker); err != nil {
		t.Fatal(err)
	}
	// A new remote identity change must still block commit on replay.
	fake.mu.Lock()
	fake.blockNewLogin = false
	fake.identityChanged = true
	fake.mu.Unlock()
	if e.Resume(context.Background(), resumePasswordFixture(t, root, panel)) == nil {
		t.Fatal("commit skipped fresh preservation check")
	}
	fake.mu.Lock()
	fake.identityChanged = false
	fake.mu.Unlock()
	if err := e.Resume(context.Background(), resumePasswordFixture(t, root, panel)); err != nil {
		t.Fatal(err)
	}
	i, err := Inspect(root)
	if err != nil || i.Plan().Status != "unchanged" {
		t.Fatal("partial commit did not recover")
	}
}

func TestPreparationAndStaleReviewCannotMutate(t *testing.T) {
	for _, reason := range []string{"backup", "two-factor", "stale"} {
		t.Run(reason, func(t *testing.T) {
			root := passwordFixture(t)
			fake, panel := newFakePanel(t)
			w, err := reviewPasswordFixture(t, root, panel)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "backup" {
				fake.badBackup = true
			}
			if reason == "two-factor" {
				fake.twoFactor = true
			}
			if reason == "stale" {
				if err := os.WriteFile(filepath.Join(root, "users.txt"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := files(t, root)
			if (deployment.Engine{Root: root}).Start(context.Background(), w) == nil {
				t.Fatal("invalid preparation succeeded")
			}
			fake.mu.Lock()
			updates := fake.updates
			fake.mu.Unlock()
			if updates != 0 {
				t.Fatal("mutation occurred without preparation")
			}
			for _, name := range passwordInputs {
				data, _ := os.ReadFile(filepath.Join(root, name))
				if string(data) != before[filepath.Join(root, name)] {
					t.Fatal("preparation changed inputs")
				}
			}
		})
	}
}

func TestPreparationResumeAndIdentityMismatch(t *testing.T) {
	root := passwordFixture(t)
	fake, panel := newFakePanel(t)
	fake.badBackup = true
	w, _ := reviewPasswordFixture(t, root, panel)
	e := deployment.Engine{Root: root}
	if e.Start(context.Background(), w) == nil {
		t.Fatal("bad backup accepted")
	}
	fake.mu.Lock()
	fake.badBackup = false
	fake.alterIdentity = true
	fake.mu.Unlock()
	if e.Resume(context.Background(), resumePasswordFixture(t, root, panel)) == nil {
		t.Fatal("identity change accepted")
	}
	i, err := Inspect(root)
	if err != nil || i.Plan().Status != "blocked" {
		t.Fatal("unfinished Go operation not visible to planner")
	}
	fake.mu.Lock()
	fake.identityChanged = false
	fake.mu.Unlock()
	if err := e.Resume(context.Background(), resumePasswordFixture(t, root, panel)); err != nil {
		t.Fatal(err)
	}
}

func TestPanelRejectsUnsafeEndpointsAndRedirects(t *testing.T) {
	for _, endpoint := range []string{"http://example.test/panel", "https://user:password@example.test", "https://example.test?token=private", "https://example.test?", "https://example.test/#private"} {
		_, err := NewPanel(endpoint)
		if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "password@example") {
			t.Fatal("unsafe endpoint accepted or echoed")
		}
	}
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	p, _ := NewPanel(redirect.URL)
	defer p.transport.CloseIdleConnections()
	if _, err := p.login(context.Background(), "user", "password"); err == nil || hit {
		t.Fatal("panel redirect was followed")
	}
}

func TestResumeRejectsChangedLocalInputsAndEndpoint(t *testing.T) {
	root := passwordFixture(t)
	fake, panel := newFakePanel(t)
	fake.blockNewLogin = true
	w, _ := reviewPasswordFixture(t, root, panel)
	e := deployment.Engine{Root: root}
	if e.Start(context.Background(), w) == nil {
		t.Fatal("expected pause")
	}
	original, _ := os.ReadFile(filepath.Join(root, "users.txt"))
	if err := os.WriteFile(filepath.Join(root, "users.txt"), []byte("manual edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if e.Resume(context.Background(), resumePasswordFixture(t, root, panel)) == nil {
		t.Fatal("local drift accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "users.txt"), original, 0600); err != nil {
		t.Fatal(err)
	}
	_, other := newFakePanel(t)
	if e.Resume(context.Background(), resumePasswordFixture(t, root, other)) == nil {
		t.Fatal("different panel accepted")
	}
	fake.mu.Lock()
	fake.blockNewLogin = false
	fake.mu.Unlock()
	if err := e.Resume(context.Background(), resumePasswordFixture(t, root, panel)); err != nil {
		t.Fatal(err)
	}
	// The snapshot should retain exactly the original input set.
	snapshot, err := w.open(operationDirectory(t, root))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, name := range passwordInputs {
		if snapshot.Inputs[name] != nil {
			names = append(names, name)
		}
	}
	if !reflect.DeepEqual(names, passwordInputs) {
		t.Fatal("snapshot input set changed")
	}
}

func TestRecoveryArtifactsCannotBeSilentlyChanged(t *testing.T) {
	for _, artifact := range []string{"state.json", "panel.db"} {
		t.Run(artifact, func(t *testing.T) {
			root := passwordFixture(t)
			fake, panel := newFakePanel(t)
			fake.blockNewLogin = true
			w, _ := reviewPasswordFixture(t, root, panel)
			e := deployment.Engine{Root: root}
			if e.Start(context.Background(), w) == nil {
				t.Fatal("expected verification pause")
			}
			path := filepath.Join(operationDirectory(t, root), artifact)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			corrupt := append([]byte(nil), original...)
			if artifact == "state.json" {
				var snapshot passwordSnapshot
				if json.Unmarshal(original, &snapshot) != nil {
					t.Fatal("bad fixture")
				}
				snapshot.Targets[".env"] = []byte("PANEL_PASS=unexpected\n")
				corrupt, _ = json.Marshal(snapshot)
			} else {
				corrupt[len(corrupt)-1] ^= 1
			}
			if err := deployment.WriteFile(path, corrupt); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			fake.blockNewLogin = false
			fake.mu.Unlock()
			if e.Resume(context.Background(), resumePasswordFixture(t, root, panel)) == nil {
				t.Fatal("changed recovery artifact accepted")
			}
			if err := deployment.WriteFile(path, original); err != nil {
				t.Fatal(err)
			}
			if err := e.Resume(context.Background(), resumePasswordFixture(t, root, panel)); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			updates := fake.updates
			fake.mu.Unlock()
			if updates != 1 {
				t.Fatal("recovery corruption caused repeated mutation")
			}
		})
	}
}

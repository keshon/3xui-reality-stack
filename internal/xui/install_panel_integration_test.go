//go:build integration

package xui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
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
	for _, name := range []string{"xui/db", "shared/certs"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
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
	b, err := newPanelBootstrap(config{Env: map[string]string{
		"PANEL_USER": "integration-admin", "PANEL_PASS": "integration-private-" + owner,
		"PUBLIC_DOMAIN": "integration.example.test", "PANEL_PATH": "/integration-panel", "SUB_PATH": "/integration-sub", "JSON_PATH": "/integration-json",
	}})
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
	if err := validateBootstrapDatabase(filepath.Join(root, "xui/db")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "xui/db/x-ui.db")); err != nil || info.Size() == 0 {
		t.Fatal("bootstrap did not initialize its private database")
	}
}

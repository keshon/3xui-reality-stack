//go:build integration

package xui

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// Explicitly opt in on an existing Linux installation. This test performs
// authenticated reads only: no configure, review, apply, restart or restore.
// Cross-compile with go test -c; the VPS does not need Go or test dependencies.
func TestLivePanelReadOnly(t *testing.T) {
	root := os.Getenv("STACK_TEST_LIVE_ROOT")
	if root == "" {
		t.Skip("live panel checks require STACK_TEST_LIVE_ROOT")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("live checks require the VPS's local Linux runtime")
	}
	unlock, err := deployment.Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	inputs, err := readPasswordInputs(root)
	if err != nil {
		t.Fatal(err)
	}
	stack := Stack{}
	report, before, err := stack.observeRuntime(t.Context(), root)
	if err != nil || report.Status != "compatible" {
		t.Fatal("live runtime is not compatible")
	}
	var applied baseline
	if json.Unmarshal(inputs[".stack/applied.json"], &applied) != nil {
		t.Fatal("invalid live baseline")
	}
	panel, err := managedConfigurationPanel(applied.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer panel.transport.CloseIdleConnections()
	session, err := panel.login(t.Context(), applied.Config.Env["PANEL_USER"], applied.Config.Env["PANEL_PASS"])
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := session.preservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.database(t.Context()); err != nil {
		t.Fatal(err)
	}
	afterPreserved, err := session.preservation(t.Context())
	if err != nil || !bytes.Equal(preserved, afterPreserved) {
		t.Fatal("panel identities/settings changed during authenticated reads")
	}
	afterInputs, err := readPasswordInputs(root)
	if err != nil || !reflect.DeepEqual(inputs, afterInputs) {
		t.Fatal("managed files changed during authenticated reads")
	}
	report, after, err := stack.observeRuntime(t.Context(), root)
	if err != nil || report.Status != "compatible" || !reflect.DeepEqual(before, after) {
		t.Fatal("runtime changed during authenticated reads")
	}
	t.Log("Go authentication, preservation reads and database export passed; no deployment writes performed")
}

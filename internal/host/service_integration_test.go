//go:build integration

package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestServiceSystemdSyntax(t *testing.T) {
	if os.Getenv("STACK_TEST_SYSTEMD") != "1" {
		t.Skip("set STACK_TEST_SYSTEMD=1 for systemd-analyze syntax validation")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("systemd syntax validation requires Linux")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeDir := filepath.Join(t.TempDir(), `literal space %n $HOME "quoted"`)
	if err := os.Mkdir(writeDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := RenderService(SupervisedCommand{Executable: binary, Arguments: []string{"not-executed", `literal %n $HOME "quotes"`}, WriteDirectory: writeDir})
	if err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(t.TempDir(), "stack-controller-syntax-test.service")
	if err := os.WriteFile(unit, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	// Verification parses the unit; it does not install, enable or start it.
	output, err := exec.CommandContext(ctx, "systemd-analyze", "verify", unit).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd rejected generated unit: %s", output)
	}
}

func TestServiceOwnershipProbe(t *testing.T) {
	if os.Getenv("STACK_TEST_SYSTEMD") != "1" {
		t.Skip("set STACK_TEST_SYSTEMD=1 for system manager observation")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("system manager observation requires Linux")
	}
	var identity [12]byte
	if _, err := rand.Read(identity[:]); err != nil {
		t.Fatal(err)
	}
	name := "stackctl-probe-" + hex.EncodeToString(identity[:]) + ".service"
	state, err := ObserveService(t.Context(), name)
	if err != nil || !state.Absent() {
		t.Fatal("system manager did not report an unused synthetic service as absent")
	}
}

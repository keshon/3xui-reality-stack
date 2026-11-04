package xui

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise the embedded shell's real lifecycle with local process boundaries.
// This does not pretend to establish a working proxy transport.
func TestTransferScriptCleansCredentialsAndCoreOnSuccessAndFailure(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if runtime.GOOS == "windows" {
		bash = "C:/Program Files/Git/bin/bash.exe"
		_, err = os.Stat(bash)
	}
	if err != nil {
		t.Skip("Bash unavailable")
	}
	unixPath := func(path string) string {
		path = filepath.ToSlash(path)
		if len(path) > 2 && path[1] == ':' {
			return "/" + strings.ToLower(path[:1]) + path[2:]
		}
		return path
	}
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("xray", `#!/bin/sh
set -eu
printf '%s' "$3" > "$PROBE_TEST_DIR/config-path"
trap 'printf stopped > "$PROBE_TEST_DIR/stopped"; exit 0' TERM
cat "$3" > "$PROBE_TEST_DIR/input"
while :; do sleep 0.1; done
`)
			write("curl", `#!/bin/sh
set -eu
for attempt in 1 2 3 4 5 6 7 8 9 10; do
    [ -f "$PROBE_TEST_DIR/input" ] && break
    sleep 0.1
done
[ -f "$PROBE_TEST_DIR/input" ]
[ "$PROBE_TEST_FAIL" = 0 ] || exit 7
printf '200 70000'
`)
			// Replace only the hard-coded binary path at the process boundary.
			write("probe.sh", strings.ReplaceAll(transferScript, `"/app/bin/xray-linux-$1"`, `"$PROBE_TEST_DIR/xray"`))
			cmd := exec.CommandContext(t.Context(), bash, "-c", `export PATH="$PROBE_TEST_DIR:$PATH"; exec sh "$PROBE_TEST_DIR/probe.sh" amd64`)
			flag := "0"
			if failure {
				flag = "1"
			}
			cmd.Env = append(os.Environ(), "PROBE_TEST_DIR="+unixPath(dir), "PROBE_TEST_FAIL="+flag)
			cmd.Stdin = strings.NewReader(`{"private":"test-only-credential"}`)
			output, err := cmd.CombinedOutput()
			if (err != nil) != failure {
				t.Fatalf("unexpected probe outcome: %v %s", err, output)
			}
			if !failure && string(output) != "200 70000" {
				t.Fatalf("unexpected probe output: %s", output)
			}
			input, err := os.ReadFile(filepath.Join(dir, "input"))
			if err != nil || string(input) != `{"private":"test-only-credential"}` {
				t.Fatal("probe did not receive private stdin")
			}
			if _, err := os.Stat(filepath.Join(dir, "stopped")); err != nil {
				t.Fatal("probe core survived cleanup")
			}
			configPath, err := os.ReadFile(filepath.Join(dir, "config-path"))
			if err != nil {
				t.Fatal(err)
			}
			check := exec.CommandContext(t.Context(), bash, "-c", `test ! -e "$1"`, "probe-cleanup", string(configPath))
			if err := check.Run(); err != nil {
				t.Fatal("probe credentials survived cleanup")
			}
		})
	}
}

package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func shellPath(path string) string {
	path = filepath.ToSlash(path)
	if len(path) > 2 && path[1] == ':' {
		return "/" + strings.ToLower(path[:1]) + path[2:]
	}
	return path
}

type installation struct {
	bash, repository, directory, prefix, root, fixture string
	env                                                []string
}

func newInstallation(t *testing.T) installation {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if runtime.GOOS == "windows" {
		bash = "C:/Program Files/Git/bin/bash.exe"
		_, err = os.Stat(bash)
	}
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tools, fixture := filepath.Join(dir, "tools"), filepath.Join(dir, "assets")
	for _, path := range []string{tools, fixture, filepath.Join(dir, "deployment")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(tools, "uname"), "#!/usr/bin/env bash\ncase \"$1\" in -s) echo \"${BOOTSTRAP_OS:-Linux}\";; -m) echo \"${BOOTSTRAP_ARCH:-x86_64}\";; *) exit 2;; esac\n")
	write(filepath.Join(tools, "curl"), `#!/usr/bin/env bash
set -eu
output= url=
while (($#)); do
    if [[ $1 == --output ]]; then output=$2; shift 2; else url=$1; shift; fi
done
asset=${url##*/}
printf '%s\n' "$asset" >> "$BOOTSTRAP_CALLS"
[[ $url == https://github.com/keshon/3xui-reality-stack/releases/download/controller-v0.1.0-go.1/* ]] || exit 3
[[ ${BOOTSTRAP_DOWNLOAD_FAIL:-0} != 1 ]] || exit 22
cp -- "$BOOTSTRAP_FIXTURE/$asset" "$output"
`)
	binary := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$1" >> "$BOOTSTRAP_EXECUTIONS"
case "$1" in
    version) echo v0.1.0-go.1 ;;
    inspect) [[ $2 == --root && $# == 3 ]]; printf '{"managed":true}\n' ;;
    *) exit 9 ;;
esac
`
	write(filepath.Join(fixture, "stackctl-go_linux_amd64"), binary)
	sum := sha256.Sum256([]byte(binary))
	write(filepath.Join(fixture, "SHA256SUMS"), hex.EncodeToString(sum[:])+"  stackctl-go_linux_amd64\n")
	root := filepath.Join(dir, "deployment")
	write(filepath.Join(root, "existing-state"), "must survive\n")
	env := os.Environ()
	env = append(env, "BOOTSTRAP_TOOLS="+shellPath(tools),
		"BOOTSTRAP_FIXTURE="+shellPath(fixture), "BOOTSTRAP_CALLS="+shellPath(filepath.Join(dir, "downloads")), "BOOTSTRAP_EXECUTIONS="+shellPath(filepath.Join(dir, "executions")))
	return installation{bash, repository, dir, filepath.Join(dir, "controller"), root, fixture, env}
}

func (i installation) run(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	args := []string{"scripts/install-controller.sh", "--version", "v0.1.0-go.1", "--prefix", shellPath(i.prefix), "--root", shellPath(i.root)}
	// Set the Unix PATH inside Bash: MSYS may normalize a Windows PATH supplied
	// by the parent process, which would otherwise bypass these local fixtures.
	command := []string{"-c", `export PATH="$BOOTSTRAP_TOOLS:$PATH"; exec bash "$@"`, "bootstrap-test"}
	cmd := exec.Command(i.bash, append(command, append(args, extra...)...)...)
	cmd.Dir, cmd.Env = i.repository, i.env
	data, err := cmd.CombinedOutput()
	return string(data), err
}

func TestBootstrapInstallsVerifiedVersionAndPreservesDeployment(t *testing.T) {
	i := newInstallation(t)
	if out, err := i.run(t); err != nil {
		t.Fatalf("bootstrap: %s", out)
	}
	installed := filepath.Join(i.prefix, "v0.1.0-go.1", "stackctl-go")
	before, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal("controller was not installed at its versioned path")
	}
	if out, err := i.run(t, "--inspect"); err != nil || !strings.Contains(out, `"managed":true`) {
		t.Fatalf("bootstrap rerun/inspection: %s", out)
	}
	after, _ := os.ReadFile(installed)
	if string(before) != string(after) {
		t.Fatal("rerun changed an existing release")
	}
	state, _ := os.ReadFile(filepath.Join(i.root, "existing-state"))
	if string(state) != "must survive\n" {
		t.Fatal("bootstrap modified deployment state")
	}
	entries, _ := os.ReadDir(i.root)
	if len(entries) != 1 {
		t.Fatal("bootstrap created files inside the deployment")
	}
	executions, _ := os.ReadFile(filepath.Join(i.directory, "executions"))
	if string(executions) != "version\nversion\ninspect\n" {
		t.Fatal("bootstrap executed an unexpected operation")
	}
	if _, err := os.Stat(filepath.Join(i.prefix, ".install-lock")); !os.IsNotExist(err) {
		t.Fatal("completed install retained its lock")
	}
}

func TestBootstrapRejectsCorruptionBeforeExecution(t *testing.T) {
	for _, scenario := range []string{"checksum", "duplicate", "missing", "download", "architecture", "os", "version"} {
		t.Run(scenario, func(t *testing.T) {
			i := newInstallation(t)
			manifest := filepath.Join(i.fixture, "SHA256SUMS")
			data, _ := os.ReadFile(manifest)
			switch scenario {
			case "checksum":
				if data[0] == '0' {
					data[0] = '1'
				} else {
					data[0] = '0'
				}
				os.WriteFile(manifest, data, 0600)
			case "duplicate":
				os.WriteFile(manifest, append(data, data...), 0600)
			case "missing":
				os.WriteFile(manifest, []byte(""), 0600)
			case "download":
				i.env = append(i.env, "BOOTSTRAP_DOWNLOAD_FAIL=1")
			case "architecture":
				i.env = append(i.env, "BOOTSTRAP_ARCH=riscv64")
			case "os":
				i.env = append(i.env, "BOOTSTRAP_OS=Darwin")
			case "version":
				out, err := i.run(t, "--version", "v01.0.0")
				if err == nil {
					t.Fatalf("invalid version accepted: %s", out)
				}
				if _, err := os.Stat(filepath.Join(i.directory, "downloads")); !os.IsNotExist(err) {
					t.Fatal("invalid version reached the download client")
				}
				return
			}
			if out, err := i.run(t); err == nil {
				t.Fatalf("invalid download/platform accepted: %s", out)
			}
			if _, err := os.Stat(filepath.Join(i.directory, "executions")); !os.IsNotExist(err) {
				t.Fatal("unverified binary executed")
			}
			if _, err := os.Stat(i.prefix); !os.IsNotExist(err) {
				t.Fatal("invalid artifact created an installation")
			}
		})
	}
}

func TestBootstrapRefusesExistingReleaseChangesAndCheckoutInstall(t *testing.T) {
	i := newInstallation(t)
	if out, err := i.run(t); err != nil {
		t.Fatalf("bootstrap: %s", out)
	}
	installed := filepath.Join(i.prefix, "v0.1.0-go.1", "stackctl-go")
	if err := os.WriteFile(installed, []byte("local replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := i.run(t); err == nil {
		t.Fatalf("existing release overwritten: %s", out)
	}
	data, _ := os.ReadFile(installed)
	if string(data) != "local replacement" {
		t.Fatal("changed release was overwritten")
	}
	if out, err := i.run(t, "--prefix", shellPath(i.root)); err == nil {
		t.Fatalf("checkout install accepted: %s", out)
	}
}

func TestBootstrapChecksBinaryVersionAfterItsChecksum(t *testing.T) {
	i := newInstallation(t)
	path := filepath.Join(i.fixture, "stackctl-go_linux_amd64")
	data, _ := os.ReadFile(path)
	data = []byte(strings.ReplaceAll(string(data), "echo v0.1.0-go.1", "echo v0.1.0-wrong"))
	if err := os.WriteFile(path, data, 0755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(i.fixture, "SHA256SUMS"), []byte(hex.EncodeToString(sum[:])+"  stackctl-go_linux_amd64\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := i.run(t); err == nil {
		t.Fatalf("wrong build version installed: %s", out)
	}
	if _, err := os.Stat(i.prefix); !os.IsNotExist(err) {
		t.Fatal("wrong version created an installation")
	}
}

func TestBootstrapRefusesIncompleteInstalledManifest(t *testing.T) {
	for _, missing := range []bool{false, true} {
		i := newInstallation(t)
		if out, err := i.run(t); err != nil {
			t.Fatalf("bootstrap: %s", out)
		}
		manifest := filepath.Join(i.prefix, "v0.1.0-go.1", "SHA256SUMS")
		if missing {
			if err := os.Remove(manifest); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(manifest, []byte("changed manifest\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := i.run(t); err == nil {
			t.Fatalf("incomplete installed release accepted: %s", out)
		}
	}
}

func TestBootstrapSelectsArm64Asset(t *testing.T) {
	i := newInstallation(t)
	i.env = append(i.env, "BOOTSTRAP_ARCH=aarch64")
	data, _ := os.ReadFile(filepath.Join(i.fixture, "stackctl-go_linux_amd64"))
	if err := os.WriteFile(filepath.Join(i.fixture, "stackctl-go_linux_arm64"), data, 0755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(i.fixture, "SHA256SUMS"), []byte(hex.EncodeToString(sum[:])+"  stackctl-go_linux_arm64\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := i.run(t); err != nil {
		t.Fatalf("arm64 artifact selection: %s", out)
	}
	calls, _ := os.ReadFile(filepath.Join(i.directory, "downloads"))
	if string(calls) != "SHA256SUMS\nstackctl-go_linux_arm64\n" {
		t.Fatal("bootstrap selected the wrong architecture asset")
	}
}

func TestBootstrapDoesNotRemoveAnotherInstallLock(t *testing.T) {
	i := newInstallation(t)
	lock := filepath.Join(i.prefix, ".install-lock")
	if err := os.MkdirAll(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := i.run(t); err == nil {
		t.Fatalf("install bypassed existing lock: %s", out)
	}
	if info, err := os.Stat(lock); err != nil || !info.IsDir() {
		t.Fatal("installer removed another install's lock")
	}
	if _, err := os.Stat(filepath.Join(i.prefix, "v0.1.0-go.1")); !os.IsNotExist(err) {
		t.Fatal("locked install wrote a release")
	}
}

func TestBootstrapRejectsSymlinkedPrefix(t *testing.T) {
	i := newInstallation(t)
	target := filepath.Join(i.directory, "prefix-target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, i.prefix); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if out, err := i.run(t); err == nil {
		t.Fatalf("symlinked prefix accepted: %s", out)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Fatal("installer wrote through a symlink")
	}
}

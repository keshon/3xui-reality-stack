package deployment

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func python(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("STACK_TEST_PYTHON"); path != "" {
		return path
	}
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("Python unavailable for controller interoperability test")
	return ""
}

func TestPythonAndGoShareDeploymentLock(t *testing.T) {
	interpreter := python(t)
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script := `import sys
from pathlib import Path
from stackctl.config import Store
try:
    with Store(Path(sys.argv[1])).lock():
        print("acquired", flush=True)
        sys.stdin.readline()
except ValueError:
    print("blocked", flush=True)
`
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, interpreter, "-c", script, root)
	cmd.Dir = filepath.Join("..", "..")
	data, err := cmd.Output()
	unlock()
	if err != nil || strings.TrimSpace(string(data)) != "blocked" {
		t.Fatal("Python did not respect the Go OS lock")
	}
	cmd = exec.CommandContext(ctx, interpreter, "-c", script, root)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "acquired" {
		t.Fatal("Python could not acquire OS lock")
	}
	if other, err := Lock(root); err == nil {
		other()
		t.Fatal("Go did not respect the Python OS lock")
	}
}

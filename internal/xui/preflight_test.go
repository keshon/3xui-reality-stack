package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

func clearPreflightHost(ctx context.Context, ports []deployment.Port) (host.Observation, error) {
	return host.Observation{Facts: deployment.HostFacts{OS: "linux", Architecture: "amd64", Distribution: "ubuntu", Ports: []deployment.PortObservation{}}, Checks: []deployment.Check{{ID: "simulated-host", OK: true}}}, ctx.Err()
}

func TestPreflightNeverTreatsExistingStateAsFresh(t *testing.T) {
	for _, scenario := range []string{"absent", "empty", "managed", "legacy", "unmanaged", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "target")
			if scenario != "absent" {
				if scenario == "unknown" {
					if err := os.WriteFile(root, []byte("private-data"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			switch scenario {
			case "managed":
				os.Mkdir(filepath.Join(root, ".stack"), 0700)
			case "legacy":
				os.WriteFile(filepath.Join(root, ".env"), []byte("private-password"), 0600)
			case "unmanaged":
				os.WriteFile(filepath.Join(root, "existing-app"), []byte("private-password"), 0600)
			}
			stack := Stack{preflightHost: clearPreflightHost}
			report, err := stack.Preflight(context.Background(), root)
			if err != nil || report.Installation != scenario {
				t.Fatal("preflight misclassified installation root")
			}
			candidate := scenario == "absent" || scenario == "empty"
			if (report.Status == "candidate") != candidate {
				t.Fatal("existing state was accepted for fresh deployment")
			}
			public, _ := json.Marshal(report)
			if bytes.Contains(public, []byte("private-")) || bytes.Contains(public, []byte(root)) {
				t.Fatal("preflight exposed state contents or private root")
			}
			if scenario == "absent" {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("preflight created a deployment root")
				}
			} else if scenario != "unknown" {
				entries, _ := os.ReadDir(root)
				want := 1
				if scenario == "empty" {
					want = 0
				}
				if len(entries) != want {
					t.Fatal("preflight wrote installation state")
				}
			}
		})
	}
}

func TestPreflightLocalAndRemoteReportsMatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	controller := control.Controller{Root: root, Stack: Stack{preflightHost: clearPreflightHost}}
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
	local, err := controller.Preflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := client.Preflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(local)
	b, _ := json.Marshal(remote)
	if !bytes.Equal(a, b) || remote.Status != "candidate" {
		t.Fatal("remote preflight changed the observed report")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("remote preflight created managed state")
	}
}

func TestPreflightRejectsUnknownHostAndRemotePathParameters(t *testing.T) {
	root := t.TempDir()
	stack := Stack{preflightHost: func(ctx context.Context, ports []deployment.Port) (host.Observation, error) {
		return host.Observation{Checks: []deployment.Check{{ID: "unknown-host", Reason: "unavailable"}}}, nil
	}}
	report, err := stack.Preflight(context.Background(), root)
	if err != nil || report.Status != "blocked" {
		t.Fatal("unknown host inventory accepted")
	}
	var out bytes.Buffer
	input := strings.NewReader("{\"version\":1,\"id\":1,\"method\":\"preflight\",\"params\":{\"root\":\"/private-root\"}}\n")
	if err := control.Serve(context.Background(), input, &out, control.Controller{Root: root, Stack: stack}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "invalid_params") || strings.Contains(out.String(), "private-root") {
		t.Fatal("preflight accepted or echoed a caller-selected root")
	}
}

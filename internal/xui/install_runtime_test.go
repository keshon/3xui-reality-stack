package xui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

type runtimeDockerFixture struct {
	network              *installNetwork
	creates, validations int
	inspectFailure       bool
}

func (f *runtimeDockerFixture) run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if args[0] == "compose" {
		if args[len(args)-2] != "config" || args[len(args)-1] != "--quiet" {
			return nil, errors.New("unexpected Compose mutation")
		}
		f.validations++
		return nil, nil
	}
	switch args[1] {
	case "ls":
		if f.network == nil {
			return []byte{}, nil
		}
		return json.Marshal(map[string]string{"id": f.network.ID, "name": "proxy"})
	case "inspect":
		if f.inspectFailure {
			f.inspectFailure = false
			return nil, errors.New("interrupted observation")
		}
		if f.network == nil || args[2] != f.network.ID {
			return nil, errors.New("wrong immutable network identity")
		}
		return json.Marshal(f.network)
	case "create":
		if f.network != nil {
			return nil, errors.New("network already exists")
		}
		f.creates++
		network := &installNetwork{ID: strings.Repeat("a", 64), Name: "proxy", Driver: "bridge", Scope: "local"}
		network.Subnets = append(network.Subnets, struct{ Subnet, Gateway string }{"172.28.0.0/16", "172.28.0.1"})
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--label" {
				key, value, _ := strings.Cut(args[i+1], "=")
				if key == "io.stackctl.operation" {
					network.Operation = value
				} else if key == "io.stackctl.installation" {
					network.Installation = value
				}
			}
		}
		f.network = network
		return nil, errors.New("synthetic lost create reply")
	}
	return nil, errors.New("unexpected Docker command")
}

type runtimeHelperFixture struct {
	installed   bool
	activations int
}

func (f *runtimeHelperFixture) Ensure(context.Context, string, []byte, string) error {
	if !f.installed {
		f.activations++
		f.installed = true
	}
	return nil
}

func runtimeWorkflowFixture(t *testing.T) (*installRuntime, *runtimeDockerFixture, *runtimeHelperFixture) {
	t.Helper()
	root, stack := installReviewFixture(t)
	docker := &runtimeDockerFixture{}
	helper := &runtimeHelperFixture{}
	stack.docker = docker.run
	stack.preflightHost = func(ctx context.Context, ports []deployment.Port) (host.Observation, error) {
		observed, err := clearPreflightHost(ctx, ports)
		if docker.network != nil {
			observed.Facts.CustomNetworks = 1
		}
		observed.Checks = []deployment.Check{{ID: "platform", OK: true}, {ID: "existing-networks", OK: docker.network == nil}}
		return observed, err
	}
	original := stack.installHost
	stack.installHost = func(ctx context.Context, root, domain string) (installHostObservation, error) {
		observed, err := original(ctx, root, domain)
		if helper.installed {
			observed.ServiceState = host.ServiceObservation{LoadState: "loaded", ActiveState: "active", FileState: "enabled", Fragment: "/etc/systemd/system/" + observed.Service.Name}
		}
		return observed, err
	}
	spec, err := stack.ReviewState(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := decodeInstallBinding(spec.Binding)
	if err != nil {
		t.Fatal(err)
	}
	return &installRuntime{root: root, stack: stack, binding: &binding, helper: helper}, docker, helper
}

type runtimeFailure struct {
	*installRuntime
	after  string
	failed bool
}

func (f *runtimeFailure) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	steps, err := f.installRuntime.Steps(ctx, dir)
	for i := range steps {
		if steps[i].ID == f.after {
			original := steps[i].Run
			steps[i].Run = func(ctx context.Context) error {
				if err := original(ctx); err != nil {
					return err
				}
				if !f.failed {
					f.failed = true
					return errors.New("effect succeeded before checkpoint")
				}
				return nil
			}
		}
	}
	return steps, err
}

func TestInstallRuntimeRecoversEffectsBeforeCheckpointWithoutClaimingCompletion(t *testing.T) {
	for _, stage := range []string{"network", "certificate-service"} {
		t.Run(stage, func(t *testing.T) {
			workflow, docker, helper := runtimeWorkflowFixture(t)
			engine := deployment.Engine{Root: workflow.root}
			fault := &runtimeFailure{installRuntime: workflow, after: stage}
			if err := engine.Start(t.Context(), fault); err == nil {
				t.Fatal("lost checkpoint was ignored")
			}
			resumed := &installRuntime{root: workflow.root, stack: workflow.stack, helper: helper}
			if err := engine.Resume(t.Context(), resumed); err == nil || !strings.Contains(err.Error(), "services") {
				t.Fatal("unfinished service stage was not blocked")
			}
			if docker.creates != 1 || helper.activations != 1 || docker.validations != 3 {
				t.Fatal("recovery duplicated runtime effects or validation")
			}
			status, err := engine.Status()
			if err != nil || status.Status != "failed" {
				t.Fatal("unfinished installation claimed completion")
			}
			if _, err := os.Stat(filepath.Join(workflow.root, ".stack/applied.json")); !os.IsNotExist(err) {
				t.Fatal("runtime preparation created applied baseline")
			}
		})
	}
}

func TestInstallRuntimeRefusesChangedInputsOrReplacedOwnedNetwork(t *testing.T) {
	for _, fault := range []string{"input", "network-id", "controller"} {
		t.Run(fault, func(t *testing.T) {
			workflow, docker, helper := runtimeWorkflowFixture(t)
			engine := deployment.Engine{Root: workflow.root}
			if err := engine.Start(t.Context(), &runtimeFailure{installRuntime: workflow, after: "network"}); err == nil {
				t.Fatal("expected synthetic interruption")
			}
			switch fault {
			case "input":
				os.WriteFile(filepath.Join(workflow.root, "users.txt"), []byte("unexpected-account"), 0600)
			case "network-id":
				docker.network.ID = strings.Repeat("b", 64)
			case "controller":
				original := workflow.stack.installHost
				workflow.stack.installHost = func(ctx context.Context, root, domain string) (installHostObservation, error) {
					observed, err := original(ctx, root, domain)
					observed.Controller.SHA256 = strings.Repeat("b", 64)
					return observed, err
				}
			}
			resumed := &installRuntime{root: workflow.root, stack: workflow.stack, helper: helper}
			if err := engine.Resume(t.Context(), resumed); err == nil {
				t.Fatal("changed installation binding was accepted")
			}
			if helper.activations != 0 || docker.creates != 1 {
				t.Fatal("changed binding triggered further host effects")
			}
		})
	}
}

func TestOwnedNetworkReconcilesLostReplyButRejectsReplacementAndForeignLabels(t *testing.T) {
	root := "/opt/test-stack"
	owner := strings.Repeat("c", 32)
	docker := &runtimeDockerFixture{}
	id, err := ensureInstallNetwork(t.Context(), docker.run, root, owner, "")
	if err != nil || docker.creates != 1 {
		t.Fatal("lost create reply did not reconcile")
	}
	if _, err := ensureInstallNetwork(t.Context(), docker.run, root, owner, id); err != nil || docker.creates != 1 {
		t.Fatal("network reconciliation recreated a resource")
	}
	docker.network.Operation = strings.Repeat("d", 32)
	if _, err := ensureInstallNetwork(t.Context(), docker.run, root, owner, id); err == nil {
		t.Fatal("foreign network was adopted")
	}
	docker.network = nil
	if _, err := ensureInstallNetwork(t.Context(), docker.run, root, owner, id); err == nil || docker.creates != 1 {
		t.Fatal("missing recorded network was silently replaced")
	}
}

func TestInstallRuntimeRecoversNetworkCreatedBeforeReceipt(t *testing.T) {
	workflow, docker, helper := runtimeWorkflowFixture(t)
	docker.inspectFailure = true
	engine := deployment.Engine{Root: workflow.root}
	if err := engine.Start(t.Context(), workflow); err == nil {
		t.Fatal("expected interrupted network inspection")
	}
	status, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if docker.creates != 1 {
		t.Fatal("network was not created before the interrupted inspection")
	}
	if _, err := os.Stat(filepath.Join(workflow.root, ".stack/go-operations", status.ID, "network.json")); !os.IsNotExist(err) {
		t.Fatal("network receipt appeared before successful inspection")
	}
	resumed := &installRuntime{root: workflow.root, stack: workflow.stack, helper: helper}
	if err := engine.Resume(t.Context(), resumed); err == nil || !strings.Contains(err.Error(), "services") {
		t.Fatal("runtime did not reconcile the owned network and reach pending services")
	}
	if docker.creates != 1 || helper.activations != 1 {
		t.Fatal("recovery duplicated network creation or helper activation")
	}
}

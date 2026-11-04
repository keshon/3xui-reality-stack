package xui

import (
	"bytes"
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

func installReviewFixture(t *testing.T) (string, Stack) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "draft")
	input := validInitialInput()
	input.PanelPassword = "sensitive-initial-password"
	if _, err := (Stack{}).Initialize(t.Context(), root, rawInitialInput(t, input)); err != nil {
		t.Fatal(err)
	}
	s := Stack{preflightHost: clearPreflightHost, installHost: func(context.Context, string, string) (installHostObservation, error) {
		return installHostObservation{
			Controller:   controllerObservation{Path: "/opt/releases/v0.1.0-go.1/stackctl-go", SHA256: strings.Repeat("a", 64), Version: "v0.1.0-go.1"},
			Service:      serviceFile{Name: "stackctl-certs-fixture.service", Data: []byte("private-service-unit-binding")},
			ServiceState: host.ServiceObservation{LoadState: "not-found", ActiveState: "inactive"},
		}, nil
	}}
	return root, s
}

func TestInstallReviewBindsDraftReleaseHostAndUnitWithoutEnablingApply(t *testing.T) {
	root, stack := installReviewFixture(t)
	before := files(t, root)
	spec, err := stack.ReviewState(t.Context(), root)
	if err != nil || spec.Kind != "xui-install-v1" || spec.Plan.Status != "proposed" {
		t.Fatal("fresh-install review failed")
	}
	var binding installBinding
	if json.Unmarshal(spec.Binding, &binding) != nil || len(binding.Inputs) != len(initializationFiles)+1 || binding.Helper.Controller.SHA256 != strings.Repeat("a", 64) || binding.Host.OS != "linux" {
		t.Fatal("review omitted exact installation binding")
	}
	if !bytes.Contains(binding.Inputs[".stack/config.json"], []byte("sensitive-initial-password")) || len(binding.Helper.Service.Data) == 0 {
		t.Fatal("private reviewed inputs were lost")
	}
	e := deployment.Engine{Root: root}
	review, err := e.Review(t.Context(), stack)
	if err != nil || review.CanApply {
		t.Fatal("install review failed or enabled incomplete execution")
	}
	public, _ := json.Marshal(review)
	for _, private := range []string{"sensitive-initial-password", root, "private-service-unit-binding", strings.Repeat("a", 64), "www.example.test"} {
		if bytes.Contains(public, []byte(private)) {
			t.Fatal("public install review exposed a private binding")
		}
	}
	for path, data := range before {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != data {
			t.Fatal("review changed draft runtime inputs")
		}
	}
	if status, err := e.Status(); err != nil || status.Status != "idle" {
		t.Fatal("review registered an execution")
	}
	if _, err := e.Review(t.Context(), PasswordStack{Stack: stack}); err == nil {
		t.Fatal("password adapter enabled an install review")
	}
	if _, err := e.ApplyReview(t.Context(), review.ID, true, PasswordStack{Stack: stack}); !errors.Is(err, deployment.ErrExecutionUnavailable) {
		t.Fatal("password execution accepted installation preview")
	}
}

func TestInstallReviewRejectsHostAndOwnershipConflictsAndInputRaces(t *testing.T) {
	for _, scenario := range []string{"busy-host", "existing-service", "manager-unavailable", "input-race", "populated-db", "changed-policy", "foreign-output"} {
		t.Run(scenario, func(t *testing.T) {
			root, stack := installReviewFixture(t)
			switch scenario {
			case "busy-host":
				stack.preflightHost = func(ctx context.Context, ports []deployment.Port) (host.Observation, error) {
					return host.Observation{Checks: []deployment.Check{{ID: "existing-containers", OK: false, Reason: "existing applications"}}}, nil
				}
			case "existing-service":
				original := stack.installHost
				stack.installHost = func(ctx context.Context, root, domain string) (installHostObservation, error) {
					observation, _ := original(ctx, root, domain)
					observation.ServiceState.LoadState = "loaded"
					return observation, nil
				}
			case "manager-unavailable":
				stack.installHost = func(context.Context, string, string) (installHostObservation, error) {
					return installHostObservation{}, errors.New("private-manager-marker")
				}
			case "input-race":
				original := stack.installHost
				stack.installHost = func(ctx context.Context, root, domain string) (installHostObservation, error) {
					os.WriteFile(filepath.Join(root, "users.txt"), []byte("private-race-marker"), 0600)
					return original(ctx, root, domain)
				}
			case "populated-db":
				os.WriteFile(filepath.Join(root, "xui/db/x-ui.db"), []byte("existing-panel"), 0600)
			case "changed-policy":
				os.WriteFile(filepath.Join(root, "compatibility.json"), []byte("private-policy-marker"), 0600)
			case "foreign-output":
				// Even a self-consistent edited manifest cannot introduce arbitrary
				// deployment files that this controller would not render.
				var c config
				readJSON(filepath.Join(root, ".stack/config.json"), &c, true)
				c.Env["ROLE"] = "node"
				deployment.WriteJSON(filepath.Join(root, ".stack/config.json"), c)
				var marker initialization
				readJSON(filepath.Join(root, ".stack/go-initialization.json"), &marker, true)
				data, _ := os.ReadFile(filepath.Join(root, ".stack/config.json"))
				marker.Files[".stack/config.json"] = hashBytes(data)
				deployment.WriteJSON(filepath.Join(root, ".stack/go-initialization.json"), marker)
			}
			if _, err := stack.ReviewState(t.Context(), root); err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("install review accepted or echoed a conflict")
			}
			if _, err := os.Stat(filepath.Join(root, ".stack/applied.json")); !os.IsNotExist(err) {
				t.Fatal("failed review created applied state")
			}
		})
	}
}

func TestInstallBindingChangesWhenControllerReleaseChanges(t *testing.T) {
	root, stack := installReviewFixture(t)
	first, err := stack.ReviewState(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	original := stack.installHost
	stack.installHost = func(ctx context.Context, root, domain string) (installHostObservation, error) {
		observation, err := original(ctx, root, domain)
		observation.Controller.SHA256 = strings.Repeat("b", 64)
		return observation, err
	}
	second, err := stack.ReviewState(t.Context(), root)
	if err != nil || bytes.Equal(first.Binding, second.Binding) {
		t.Fatal("controller replacement did not change review binding")
	}
}

func TestPreflightRecognizesOnlyAnUnusedDraftAndChecksCorePorts(t *testing.T) {
	root, stack := installReviewFixture(t)
	before := files(t, root)
	stack.preflightHost = func(ctx context.Context, ports []deployment.Port) (host.Observation, error) {
		if len(ports) != 4 {
			t.Fatal("preflight checked unused optional transport ports")
		}
		return clearPreflightHost(ctx, ports)
	}
	report, err := stack.Preflight(t.Context(), root)
	if err != nil || report.Installation != "draft" || report.Status != "candidate" {
		t.Fatal("valid draft was treated as deployed state")
	}
	for path, data := range before {
		actual, _ := os.ReadFile(path)
		if string(actual) != data {
			t.Fatal("preflight changed draft")
		}
	}
	os.WriteFile(filepath.Join(root, "xui/db/x-ui.db"), []byte("existing-panel"), 0600)
	report, err = stack.Preflight(t.Context(), root)
	if err != nil || report.Status != "blocked" {
		t.Fatal("draft with live runtime state was accepted")
	}
}

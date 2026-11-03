package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestRuntimeIsRecheckedAfterAuthenticatedPreservationRead(t *testing.T) {
	root := passwordFixture(t)
	fake, panel := newFakePanel(t)
	w, err := reviewPasswordFixture(t, root, panel)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := w.Prepare(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	steps, err := w.Steps(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(root, "compatibility.json")
	data, _ := os.ReadFile(policy)
	fake.mu.Lock()
	fake.onSettings = func() {
		// Simulate a policy edit after the step's initial runtime inspection.
		_ = os.WriteFile(policy, append(data, '\n'), 0600)
	}
	fake.mu.Unlock()
	if err := steps[0].Run(context.Background()); err == nil {
		t.Fatal("mutation used only the step's initial runtime observation")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.updates != 0 {
		t.Fatal("password mutated after policy changed during preservation read")
	}
}

func TestResumeRequiresOriginalRuntimeReview(t *testing.T) {
	for _, artifact := range []string{"missing", "inputs", "runtime"} {
		t.Run(artifact, func(t *testing.T) {
			root := passwordFixture(t)
			_, panel := newFakePanel(t)
			w, err := reviewPasswordFixture(t, root, panel)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := w.Prepare(context.Background(), dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "reviewed.json")
			if artifact == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				var saved passwordReview
				if ok, err := readJSON(path, &saved, true); !ok || err != nil {
					t.Fatal("missing saved review")
				}
				if artifact == "inputs" {
					saved.Binding.Inputs["users.txt"] = []byte("changed")
				} else {
					saved.Binding.Runtime.Panel.StartedAt = "2026-01-02T00:00:00Z"
				}
				if err := deployment.WriteJSON(path, saved); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(root, ".env"))
			resumed := resumePasswordFixture(t, root, panel)
			steps, err := resumed.Steps(context.Background(), dir)
			if err == nil {
				err = steps[0].Run(context.Background())
			}
			if err == nil {
				t.Fatal("recovery accepted a missing or changed runtime review")
			}
			after, _ := os.ReadFile(filepath.Join(root, ".env"))
			if !bytes.Equal(before, after) {
				t.Fatal("invalid review changed local credentials")
			}
		})
	}
}

func TestPasswordRuntimeDriftStopsWritesAndRecovery(t *testing.T) {
	for _, stage := range []string{"preparation", "mutation", "commit", "partial-commit"} {
		for _, drift := range []string{"restart", "replacement", "image", "mount", "listener", "policy"} {
			t.Run(stage+"/"+drift, func(t *testing.T) {
				root := passwordFixture(t)
				fake, panel := newFakePanel(t)
				stack := passwordTestStack(root)
				originalDocker := stack.docker
				changed := false
				stack.docker = func(ctx context.Context, args ...string) ([]byte, error) {
					data, err := originalDocker(ctx, args...)
					if !changed || args[0] != "inspect" {
						return data, err
					}
					var c container
					if json.Unmarshal(data, &c) != nil {
						t.Fatal("bad fake runtime")
					}
					switch drift {
					case "restart":
						c.StartedAt = "2026-01-02T00:00:00Z"
					case "replacement":
						c.ID = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
					case "image":
						c.ImageID = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
					case "mount":
						c.Mounts[0].RW = false
					case "listener":
						c.Ports["8443/tcp"][0].HostIP = "0.0.0.0"
					}
					return json.Marshal(c)
				}
				w, err := stack.reviewPassword(context.Background(), root, panel)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				if stage != "preparation" {
					if err := w.Prepare(context.Background(), dir); err != nil {
						t.Fatal(err)
					}
				}
				var steps []deployment.Step
				if stage != "preparation" {
					steps, err = w.Steps(context.Background(), dir)
					if err != nil {
						t.Fatal(err)
					}
				}
				if stage == "commit" || stage == "partial-commit" {
					if err := steps[0].Run(context.Background()); err != nil {
						t.Fatal(err)
					}
					if stage == "partial-commit" {
						snapshot, err := w.open(dir)
						if err != nil {
							t.Fatal(err)
						}
						if err := deployment.WriteFile(filepath.Join(root, ".env"), snapshot.Targets[".env"]); err != nil {
							t.Fatal(err)
						}
					}
				}
				before, _ := readPasswordInputs(root)
				policyPath := filepath.Join(root, "compatibility.json")
				policy, _ := os.ReadFile(policyPath)
				changed = true
				if drift == "policy" {
					if err := os.WriteFile(policyPath, append(policy, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "preparation" {
					err = w.Prepare(context.Background(), dir)
				} else if stage == "mutation" {
					err = steps[0].Run(context.Background())
				} else {
					// Reconstruct the workflow from disk, as a new process would.
					resumed := ResumePassword(root, panel)
					resumed.stack = stack
					steps, err = resumed.Steps(context.Background(), dir)
					if err == nil {
						err = steps[2].Run(context.Background())
					}
				}
				if err == nil {
					t.Fatal("write accepted runtime drift")
				}
				for name, data := range before {
					actual, _ := os.ReadFile(filepath.Join(root, name))
					if string(actual) != string(data) {
						t.Fatal("blocked workflow changed local inputs")
					}
				}
				fake.mu.Lock()
				updates := fake.updates
				fake.mu.Unlock()
				want := 0
				if stage == "commit" || stage == "partial-commit" {
					want = 1
				}
				if updates != want {
					t.Fatal("blocked workflow changed remote password")
				}
				changed = false
				if err := os.WriteFile(policyPath, policy, 0600); err != nil {
					t.Fatal(err)
				}
				if stage != "preparation" {
					// Restoring the exact reviewed runtime permits explicit recovery.
					resumed := ResumePassword(root, panel)
					resumed.stack = stack
					recovered, err := resumed.Steps(context.Background(), dir)
					if err != nil {
						t.Fatal(err)
					}
					for _, step := range recovered {
						if err := step.Run(context.Background()); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

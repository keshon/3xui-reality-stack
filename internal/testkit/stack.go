// Package testkit provides a simulated application stack for controller tests.
// The shipping CLI does not import it. All effects are confined to a fixture
// directory; it uses no Docker, panel API, host configuration, or network.
package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Fault string

const (
	BeforeWrite  Fault = "before-write"
	AfterWrite   Fault = "after-write"
	DuringVerify Fault = "during-verify"
)

type Stack struct {
	Fault Fault
	// AfterPersist can model an abrupt process exit before the engine checkpoint.
	AfterPersist func()
}

func (s Stack) Runtime(ctx context.Context, root string) (deployment.RuntimeReport, error) {
	i, err := s.Inspect(ctx, root)
	report := deployment.RuntimeReport{Stack: "fixture-site", Status: "compatible", Checks: []deployment.Check{{ID: "fixture-runtime", OK: len(i.Report.Blockers) == 0}}, Observation: "simulated application state only"}
	if len(i.Report.Blockers) != 0 {
		report.Status = "blocked"
		report.Checks[0].Reason = "fixture runtime is absent or has drifted"
	}
	return report, err
}

type state struct {
	Value string `json:"value"`
}
type snapshot struct {
	Desired  []byte `json:"desired"`
	Applied  []byte `json:"applied"`
	Runtime  []byte `json:"runtime"`
	Identity []byte `json:"identity"`
}

func Seed(root, applied, desired string) error {
	if err := os.Mkdir(filepath.Join(root, ".fixture"), 0700); err != nil {
		return err
	}
	for name, value := range map[string]string{"desired.json": desired, "applied.json": applied, "running.json": applied} {
		if err := deployment.WriteJSON(filepath.Join(root, ".fixture", name), state{value}); err != nil {
			return err
		}
	}
	if err := deployment.WriteJSON(filepath.Join(root, ".fixture", "identity.json"), state{"initial-runtime"}); err != nil {
		return err
	}
	return nil
}

func read(root, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, ".fixture", name))
	if err != nil {
		return nil, errors.New("fixture state unavailable")
	}
	var s state
	if json.Unmarshal(data, &s) != nil {
		return nil, errors.New("fixture state invalid")
	}
	return data, nil
}

func (s Stack) Inspect(ctx context.Context, root string) (deployment.Inspection, error) {
	i := deployment.Inspection{
		Report: deployment.Report{Stack: "fixture-site", Blockers: []string{}, Observation: "simulated local application; no network checks"},
		Plan:   deployment.Plan{Stack: "fixture-site", Status: "blocked", ReadOnly: true, Actions: []deployment.Action{}},
	}
	if err := ctx.Err(); err != nil {
		return i, err
	}
	desired, err := read(root, "desired.json")
	if err != nil {
		i.Report.Blockers = append(i.Report.Blockers, "fixture configuration missing or invalid")
		i.Plan.Reasons = i.Report.Blockers
		return i, nil
	}
	i.Report.Managed = true
	applied, err := read(root, "applied.json")
	if err != nil {
		i.Report.Blockers = append(i.Report.Blockers, "fixture baseline missing or invalid")
		i.Plan.Reasons = i.Report.Blockers
		return i, nil
	}
	i.Report.Baseline = true
	running, err := read(root, "running.json")
	if err != nil || !bytes.Equal(running, applied) {
		i.Report.Blockers = append(i.Report.Blockers, "fixture runtime drift")
		i.Plan.Reasons = i.Report.Blockers
		return i, nil
	}
	if bytes.Equal(desired, applied) {
		i.Plan.Status = "unchanged"
		return i, nil
	}
	i.Plan.Status = "proposed"
	i.Plan.Actions = []deployment.Action{
		{ID: "configure-site", Resource: "site", Effects: []string{"update simulated site configuration"}},
		{ID: "verify-site", Resource: "site", Effects: []string{"verify simulated configuration"}},
		{ID: "commit-site", Resource: "managed-state", Effects: []string{"record the verified revision"}},
	}
	return i, nil
}

func (s Stack) Review(ctx context.Context, root string) (deployment.Workflow, error) {
	i, err := s.Inspect(ctx, root)
	if err != nil {
		return nil, err
	}
	if i.Plan.Status != "proposed" {
		return nil, errors.New("fixture requires an applicable plan")
	}
	frozen, err := capture(root)
	if err != nil {
		return nil, err
	}
	return &workflow{stack: s, root: root, reviewed: &frozen}, nil
}

func (s Stack) ReviewState(ctx context.Context, root string) (deployment.ReviewSpec, error) {
	before, err := capture(root)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	i, err := s.Inspect(ctx, root)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	after, err := capture(root)
	if err != nil || !equal(before, after) {
		return deployment.ReviewSpec{}, errors.New("fixture changed during review")
	}
	binding, err := json.Marshal(after)
	return deployment.ReviewSpec{Kind: "fixture-site-v1", Plan: i.Plan, Binding: binding}, err
}

func (s Stack) OpenReviewed(ctx context.Context, root, kind string, binding json.RawMessage) (deployment.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var frozen snapshot
	if kind != "fixture-site-v1" || json.Unmarshal(binding, &frozen) != nil || len(frozen.Identity) == 0 || !bytes.Equal(frozen.Runtime, frozen.Applied) {
		return nil, errors.New("fixture adapter or binding is unsupported")
	}
	return &workflow{stack: s, root: root, reviewed: &frozen}, nil
}

func (s Stack) Resume(root string) deployment.Workflow { return &workflow{stack: s, root: root} }

type workflow struct {
	stack    Stack
	root     string
	reviewed *snapshot
}

func (*workflow) Kind() string             { return "fixture-site-v1" }
func (w *workflow) DeploymentRoot() string { return w.root }

func capture(root string) (snapshot, error) {
	desired, err := read(root, "desired.json")
	if err != nil {
		return snapshot{}, err
	}
	applied, err := read(root, "applied.json")
	if err != nil {
		return snapshot{}, err
	}
	running, err := read(root, "running.json")
	if err != nil {
		return snapshot{}, err
	}
	identity, err := read(root, "identity.json")
	return snapshot{desired, applied, running, identity}, err
}

func (w *workflow) Prepare(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.reviewed == nil {
		return errors.New("fixture preparation needs a fresh review")
	}
	current, err := capture(w.root)
	if err != nil {
		return err
	}
	if !equal(current, *w.reviewed) {
		return errors.New("fixture inputs changed after review")
	}
	return deployment.WriteJSON(filepath.Join(dir, "fixture.json"), w.reviewed)
}

func equal(a, b snapshot) bool {
	return bytes.Equal(a.Desired, b.Desired) && bytes.Equal(a.Applied, b.Applied) && bytes.Equal(a.Runtime, b.Runtime) && bytes.Equal(a.Identity, b.Identity)
}

func (w *workflow) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	data, err := os.ReadFile(filepath.Join(dir, "fixture.json"))
	var frozen snapshot
	if err != nil || json.Unmarshal(data, &frozen) != nil || len(frozen.Desired) == 0 || len(frozen.Applied) == 0 || len(frozen.Identity) == 0 || !bytes.Equal(frozen.Runtime, frozen.Applied) {
		return nil, errors.New("fixture recovery state invalid")
	}
	check := func() error {
		current, err := capture(w.root)
		if err != nil {
			return err
		}
		if !bytes.Equal(current.Identity, frozen.Identity) || !bytes.Equal(current.Desired, frozen.Desired) || (!bytes.Equal(current.Applied, frozen.Applied) && !bytes.Equal(current.Applied, frozen.Desired)) || (!bytes.Equal(current.Runtime, frozen.Runtime) && !bytes.Equal(current.Runtime, frozen.Desired)) {
			return errors.New("fixture inputs changed outside operation")
		}
		return nil
	}
	verify := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
		if w.stack.Fault == DuringVerify {
			return errors.New("fixture verification failed with private details")
		}
		running, err := read(w.root, "running.json")
		if err != nil || !bytes.Equal(running, frozen.Desired) {
			return errors.New("fixture runtime differs from desired state")
		}
		return nil
	}
	return []deployment.Step{
		{ID: "configure-site", Run: func(ctx context.Context) error {
			if err := check(); err != nil {
				return err
			}
			running, err := read(w.root, "running.json")
			if err != nil {
				return err
			}
			if bytes.Equal(running, frozen.Desired) {
				return nil
			}
			if w.stack.Fault == BeforeWrite {
				return errors.New("fixture write refused with private details")
			}
			if err := deployment.WriteFile(filepath.Join(w.root, ".fixture", "running.json"), frozen.Desired); err != nil {
				return err
			}
			log, err := os.OpenFile(filepath.Join(w.root, ".fixture", "effects.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return err
			}
			_, writeErr := log.WriteString("configure-site\n")
			closeErr := log.Close()
			if writeErr != nil || closeErr != nil {
				return errors.New("fixture effect log failed")
			}
			if w.stack.AfterPersist != nil {
				w.stack.AfterPersist()
			}
			if w.stack.Fault == AfterWrite {
				return errors.New("fixture reply lost with private details")
			}
			return nil
		}},
		{ID: "verify-site", Run: verify},
		{ID: "commit-site", Run: func(ctx context.Context) error {
			if err := verify(ctx); err != nil {
				return err
			}
			return deployment.WriteFile(filepath.Join(w.root, ".fixture", "applied.json"), frozen.Desired)
		}},
	}, nil
}

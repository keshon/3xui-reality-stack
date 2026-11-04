package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type testWorkflow struct {
	root    string
	prepare func(context.Context, string) error
	steps   []Step
}

func (*testWorkflow) Kind() string             { return "fixture-v1" }
func (w *testWorkflow) DeploymentRoot() string { return w.root }
func (w *testWorkflow) Prepare(ctx context.Context, dir string) error {
	if w.prepare != nil {
		return w.prepare(ctx, dir)
	}
	return WriteJSON(filepath.Join(dir, "fixture.json"), map[string]bool{"ready": true})
}
func (w *testWorkflow) Steps(context.Context, string) ([]Step, error) { return w.steps, nil }

func loadJournal(t *testing.T, root string) journal {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".stack", "go-operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestIntentAndResumeWithoutRepeatingCompletedActions(t *testing.T) {
	root := t.TempDir()
	w := &testWorkflow{root: root}
	var calls []string
	fail := true
	for _, id := range []string{"first", "second", "third"} {
		w.steps = append(w.steps, Step{ID: id, Run: func(context.Context) error {
			j := loadJournal(t, root)
			if j.Status != "running" || j.Steps[j.Next] != id {
				t.Fatal("action intent was not persisted")
			}
			calls = append(calls, id)
			if id == "second" && fail {
				return errors.New("private-response-secret")
			}
			return nil
		}})
	}
	e := Engine{Root: root}
	err := e.Start(context.Background(), w)
	if err == nil || strings.Contains(err.Error(), "private-response-secret") {
		t.Fatal("expected a sanitized failure")
	}
	if e.Start(context.Background(), w) == nil {
		t.Fatal("unfinished operation replaced")
	}
	if e.CancelPreparation() == nil {
		t.Fatal("attempted mutation allowed preparation cancellation")
	}
	fail = false
	if err := e.Resume(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"first", "second", "second", "third"}) {
		t.Fatalf("calls: %v", calls)
	}
	if loadJournal(t, root).Status != "complete" {
		t.Fatal("journal did not complete")
	}
	if err := e.Resume(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatal("completed operation replayed")
	}
}

func TestPreparationFailureAndCancellation(t *testing.T) {
	root := t.TempDir()
	e := Engine{Root: root}
	w := &testWorkflow{root: root, prepare: func(context.Context, string) error { return errors.New("private") }}
	if e.Start(context.Background(), w) == nil {
		t.Fatal("preparation failure ignored")
	}
	if loadJournal(t, root).Status != "preparing" {
		t.Fatal("failed preparation advanced")
	}
	if err := e.CancelPreparation(); err != nil {
		t.Fatal(err)
	}
	w.prepare = nil
	w.steps = []Step{{ID: "noop", Run: func(context.Context) error { return nil }}}
	if err := e.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationStopsBeforeNextAction(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	second := false
	w := &testWorkflow{root: root, steps: []Step{
		{ID: "first", Run: func(context.Context) error { cancel(); return nil }},
		{ID: "second", Run: func(context.Context) error { second = true; return nil }},
	}}
	e := Engine{Root: root}
	if !errors.Is(e.Start(ctx, w), context.Canceled) || second {
		t.Fatal("cancellation boundary failed")
	}
	if err := e.Resume(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if !second {
		t.Fatal("cancelled operation could not resume")
	}
}

func TestDeploymentLockAndRootBinding(t *testing.T) {
	root := t.TempDir()
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Lock(root); err == nil {
		second()
		t.Fatal("concurrent lock succeeded")
	}
	unlock()
	next, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	next()
	if (Engine{Root: root}).Start(context.Background(), &testWorkflow{root: t.TempDir()}) == nil {
		t.Fatal("workflow escaped its locked root")
	}
}

func TestChangedAdapterAndLegacyWorkBlockResume(t *testing.T) {
	root := t.TempDir()
	e := Engine{Root: root}
	w := &testWorkflow{root: root, steps: []Step{{ID: "first", Run: func(context.Context) error { return errors.New("stop") }}}}
	if e.Start(context.Background(), w) == nil {
		t.Fatal("expected failure")
	}
	w.steps = []Step{{ID: "different", Run: func(context.Context) error { t.Fatal("changed adapter ran"); return nil }}}
	if e.Resume(context.Background(), w) == nil {
		t.Fatal("changed adapter resumed")
	}
	if err := WriteJSON(filepath.Join(root, ".stack", "operation.json"), map[string]string{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	if e.Resume(context.Background(), w) == nil {
		t.Fatal("legacy operation ignored")
	}
}

func TestCancelUnstartedReadyOperationRequiresExactID(t *testing.T) {
	root := t.TempDir()
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	id := strings.Repeat("a", 32)
	j := journal{Version: 1, ID: id, Kind: "fixture-v1", Status: "ready", Steps: []string{"first"}}
	if err := WriteJSON(filepath.Join(root, ".stack", "go-operation.json"), j); err != nil {
		t.Fatal(err)
	}
	e := Engine{Root: root}
	if e.CancelPreparationExpected(strings.Repeat("b", 32)) == nil {
		t.Fatal("wrong operation was cancelled")
	}
	if err := e.CancelPreparationExpected(id); err != nil {
		t.Fatal(err)
	}
	status, err := e.Status()
	if err != nil || status.Status != "cancelled" || status.CurrentStep != "" || status.Completed != 0 {
		t.Fatal("unstarted ready operation did not cancel cleanly")
	}
}

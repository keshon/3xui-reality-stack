package xui

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestReviewedPasswordWorkflowRecoversUsingOriginalBinding(t *testing.T) {
	root := passwordFixture(t)
	fake, panel := newFakePanel(t)
	adapter := passwordExecutionFixture{Stack: passwordTestStack(root), panel: panel, interrupt: "panel-password"}
	engine := deployment.Engine{Root: root}
	ctx := context.Background()
	review, err := engine.Review(ctx, adapter)
	if err != nil || !review.CanApply {
		t.Fatal("test adapter could not review password workflow")
	}
	if _, err := engine.ApplyReview(ctx, review.ID, false, adapter); err == nil {
		t.Fatal("unconfirmed password operation was accepted")
	}
	status, err := engine.ApplyReview(ctx, review.ID, true, adapter)
	if err == nil || status.Status != "failed" || status.CurrentStep != "panel-password" {
		t.Fatal("injected mutation interruption was not recoverable")
	}
	duplicate, err := engine.ApplyReview(ctx, review.ID, true, adapter)
	if err != nil || duplicate.ID != status.ID || duplicate.Status != "failed" {
		t.Fatal("duplicate apply retried or lost the failed operation")
	}
	adapter.interrupt = "commit"
	status, err = engine.ResumeReviewed(ctx, status.ID, adapter)
	if err == nil || status.Status != "failed" || status.CurrentStep != "commit" {
		t.Fatal("partial commit was not recoverable")
	}
	adapter.interrupt = ""
	status, err = engine.ResumeReviewed(ctx, status.ID, adapter)
	if err != nil || status.Status != "complete" {
		t.Fatal("reviewed workflow could not recover using original binding")
	}
	i, err := Inspect(root)
	if err != nil || i.Plan().Status != "unchanged" {
		t.Fatal("reviewed workflow did not commit baseline")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.updates != 1 {
		t.Fatal("reviewed workflow repeated the remote mutation")
	}
}

func TestOpenPasswordRejectsInvalidFrozenBinding(t *testing.T) {
	root := passwordFixture(t)
	_, panel := newFakePanel(t)
	stack := passwordTestStack(root)
	spec, err := stack.ReviewState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`), json.RawMessage(`{"unknown":true}`), append(append([]byte(nil), spec.Binding...), []byte(` {}`)...)} {
		if _, err := stack.openPassword(root, panel, raw); err == nil {
			t.Fatal("invalid review binding accepted")
		}
	}
	if _, err := stack.openPassword(root, nil, spec.Binding); err == nil {
		t.Fatal("binding opened without panel endpoint")
	}
}

// The fixture injects failures into the password workflow without enabling
// writes on the default Stack or connecting to a real management endpoint.
type passwordExecutionFixture struct {
	Stack
	panel     *Panel
	interrupt string
}

func (a passwordExecutionFixture) OpenReviewed(ctx context.Context, root, kind string, binding json.RawMessage) (deployment.Workflow, error) {
	if kind != "xui-password-v1" {
		return nil, errors.New("unsupported integration workflow")
	}
	w, err := a.Stack.openPassword(root, a.panel, binding)
	if err != nil {
		return nil, err
	}
	return interruptedPassword{PasswordChange: w, interrupt: a.interrupt}, nil
}

type interruptedPassword struct {
	*PasswordChange
	interrupt string
}

func (w interruptedPassword) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	steps, err := w.PasswordChange.Steps(ctx, dir)
	if err != nil {
		return nil, err
	}
	for n := range steps {
		if steps[n].ID != w.interrupt {
			continue
		}
		original := steps[n].Run
		steps[n].Run = func(ctx context.Context) error {
			if w.interrupt == "commit" {
				snapshot, err := w.open(dir)
				if err != nil {
					return err
				}
				if err := w.checkWrite(ctx, snapshot); err != nil {
					return err
				}
				if err := deployment.WriteFile(filepath.Join(w.root, ".env"), snapshot.Targets[".env"]); err != nil {
					return err
				}
			} else if err := original(ctx); err != nil {
				return err
			}
			return errors.New("injected interruption before checkpoint")
		}
	}
	return steps, nil
}

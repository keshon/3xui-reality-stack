package xui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestInstallAdapterReviewedApplyAndRecovery(t *testing.T) {
	root, stack := installReviewFixture(t)
	// Stop at the first Compose validation, before creating host resources.
	stack.docker = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "network" && args[1] == "ls" {
			return nil, nil
		}
		return nil, errors.New("synthetic Docker unavailable")
	}
	c := control.Controller{Root: root, Stack: InstallStack{Stack: stack}}
	plan, err := c.Plan(t.Context())
	if err != nil || plan.Status != "proposed" {
		t.Fatal("installation plan unavailable", err)
	}
	review, err := c.Review(t.Context())
	if err != nil || !review.CanApply || review.Kind != "xui-install-v1" {
		t.Fatal("installation adapter did not expose reviewed execution", err)
	}
	if _, err := c.ApplyReview(t.Context(), review.ID, false); err == nil {
		t.Fatal("installation accepted missing confirmation")
	}
	status, err := c.ApplyReview(t.Context(), review.ID, true)
	if err == nil || status.Status != "failed" || status.ID == "" || status.Kind != "xui-install-v1" {
		t.Fatal("installation did not register its recoverable failure", status, err)
	}
	// A new controller instance must reopen the original binding, not review
	// a now-active draft. An incorrect operation ID must never select it.
	c = control.Controller{Root: root, Stack: InstallStack{Stack: stack}}
	if _, err := c.ResumeOperation(t.Context(), strings.Repeat("0", 32)); err == nil {
		t.Fatal("recovery accepted a different operation")
	}
	resumed, err := c.ResumeOperation(t.Context(), status.ID)
	if err == nil || resumed.ID != status.ID || resumed.Status != "failed" {
		t.Fatal("recovery lost the exact installation operation", resumed, err)
	}
	repeated, err := c.ApplyReview(t.Context(), review.ID, true)
	if err != nil || repeated.ID != status.ID {
		t.Fatal("repeated approval started another installation", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".stack/applied.json")); !os.IsNotExist(err) {
		t.Fatal("failed installation published applied state")
	}
}

func TestInstallAdapterRejectsOtherWorkflowsAndStaleDraft(t *testing.T) {
	root, stack := installReviewFixture(t)
	adapter := InstallStack{Stack: stack}
	if _, err := adapter.OpenReviewed(t.Context(), root, "xui-password-v1", json.RawMessage(`{}`)); err == nil {
		t.Fatal("installation adapter opened a password workflow")
	}
	if _, err := adapter.OpenReviewed(t.Context(), root, "xui-install-v1", json.RawMessage(`{}`)); err == nil {
		t.Fatal("installation adapter accepted an invalid binding")
	}
	e := deployment.Engine{Root: root}
	review, err := e.Review(t.Context(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "users.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyReview(t.Context(), review.ID, true, adapter); err == nil {
		t.Fatal("installation applied a changed draft")
	}
	status, err := e.Status()
	if err != nil || status.Status != "idle" {
		t.Fatal("stale review registered an operation")
	}
	i, err := adapter.Inspect(t.Context(), root)
	if err != nil || i.Plan.Status != "blocked" {
		t.Fatal("changed draft offered an installation plan")
	}
}

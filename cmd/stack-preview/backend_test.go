package main

import (
	"slices"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/tui"
)

var _ tui.Backend = (*preview)(nil)

func TestPreviewPasswordRoundTripRequiresFreshConfirmation(t *testing.T) {
	b, _ := newPreview("healthy")
	ctx := t.Context()
	p, err := b.Configure(ctx, map[string]string{"PANEL_PASS": "preview-only-password"})
	if err != nil || p.Status != "proposed" {
		t.Fatal("preview did not propose the password change")
	}
	r, _ := b.Review(ctx)
	if _, err := b.ApplyReview(ctx, r.ID, false); err == nil {
		t.Fatal("unconfirmed preview applied")
	}
	// Editing again must invalidate the old review even in preview mode.
	b.Configure(ctx, map[string]string{"PANEL_PASS": "different-preview-password"})
	if _, err := b.ApplyReview(ctx, r.ID, true); err == nil {
		t.Fatal("stale preview review applied")
	}
	r, _ = b.Review(ctx)
	op, err := b.ApplyReview(ctx, r.ID, true)
	if err != nil || op.Status != "complete" || op.Completed != 3 {
		t.Fatal("confirmed preview did not complete")
	}
	if _, err := b.ApplyReview(ctx, r.ID, true); err == nil {
		t.Fatal("consumed preview review applied again")
	}
	p, _ = b.Plan(ctx)
	if p.Status != "unchanged" {
		t.Fatal("preview still proposed an applied change")
	}
}

func TestPreviewBlockedAndRecoveryScenarios(t *testing.T) {
	ctx := t.Context()
	b, _ := newPreview("blocked")
	r, _ := b.Runtime(ctx)
	p, _ := b.Plan(ctx)
	if r.Status != "blocked" || p.Status != "blocked" || slices.Contains(b.Methods(), "apply") {
		t.Fatal("blocked preview exposed writes")
	}
	if _, err := b.Configure(ctx, map[string]string{"PANEL_PASS": "example"}); err == nil {
		t.Fatal("blocked preview accepted changes")
	}
	for _, cancel := range []bool{false, true} {
		b, _ = newPreview("interrupted")
		op, _ := b.Operation(ctx)
		if _, err := b.ResumeOperation(ctx, "wrong-id"); err == nil {
			t.Fatal("preview resumed the wrong operation")
		}
		want := "complete"
		var err error
		if cancel {
			op, err = b.CancelPreparation(ctx, op.ID)
			want = "cancelled"
		} else {
			op, err = b.ResumeOperation(ctx, op.ID)
		}
		if err != nil || op.Status != want {
			t.Fatal("preview recovery failed")
		}
	}
	if _, err := newPreview("unknown"); err == nil {
		t.Fatal("unknown preview scenario accepted")
	}
}

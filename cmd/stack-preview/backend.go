package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// This backend simulates screen transitions, not provisioning correctness.
// No filesystem, SSH, Docker or panel clients are available here. Password
// values are validated and discarded; only the pending-change flag is kept.
type preview struct {
	blocked  bool
	pending  bool
	sequence int
	review   *deployment.Review
	op       deployment.OperationStatus
}

func newPreview(scenario string) (*preview, error) {
	b := &preview{op: deployment.OperationStatus{Status: "idle"}}
	switch scenario {
	case "healthy":
	case "blocked":
		b.blocked = true
	case "interrupted":
		b.pending = true
		b.op = deployment.OperationStatus{ID: "demo-operation", Kind: "xui-password-v1", Status: "ready", Total: 3}
	default:
		return nil, errors.New("scenario must be healthy, blocked, or interrupted")
	}
	return b, nil
}

func (b *preview) Methods() []string {
	methods := []string{"inspect", "plan", "runtime", "operation", "review"}
	if !b.blocked {
		methods = append(methods, "configure", "apply", "resume", "cancel-preparation")
	}
	return methods
}

func (b *preview) Inspect(context.Context) (deployment.Report, error) {
	r := deployment.Report{Stack: "3x-ui (mock)", Managed: true, Baseline: true}
	if b.blocked {
		r.Blockers = []string{"Mock: deployed panel version is outside the compatibility policy."}
	}
	return r, nil
}

func (b *preview) Plan(context.Context) (deployment.Plan, error) {
	p := deployment.Plan{Stack: "3x-ui (mock)", Status: "unchanged", ReadOnly: b.blocked}
	if b.blocked {
		p.Status, p.Reasons = "blocked", []string{"Mock: inspect the compatibility mismatch before making changes."}
	} else if b.pending {
		p.Status = "proposed"
		p.Actions = []deployment.Action{{ID: "panel-password", Resource: "Panel administrator password", Effects: []string{
			"Capture a recovery database and preserve existing client identities.",
			"Rotate the panel password and verify authentication.",
			"Save generated credentials and the applied baseline.",
			"Preview only: no real action will run.",
		}}}
	}
	return p, nil
}

func (b *preview) Runtime(context.Context) (deployment.RuntimeReport, error) {
	r := deployment.RuntimeReport{Stack: "3x-ui (mock)", Status: "compatible", Observation: "Synthetic results; no host or network checks were performed."}
	for _, id := range []string{"managed-state", "compatibility-policy", "panel-container", "panel-image", "database-ownership", "panel-version", "traefik-container", "management-listener"} {
		r.Checks = append(r.Checks, deployment.Check{ID: id, OK: true})
	}
	if b.blocked {
		r.Status = "blocked"
		r.Checks[5].OK, r.Checks[5].Reason = false, "Mock: unsupported panel version."
	}
	return r, nil
}

func (b *preview) Operation(context.Context) (deployment.OperationStatus, error) { return b.op, nil }

func (b *preview) Configure(ctx context.Context, changes map[string]string) (deployment.Plan, error) {
	if b.blocked || len(changes) != 1 || changes["PANEL_PASS"] == "" || len(changes["PANEL_PASS"]) > 4096 {
		return deployment.Plan{}, errors.New("invalid preview change")
	}
	b.pending, b.review = true, nil
	return b.Plan(ctx)
}

func (b *preview) Review(ctx context.Context) (deployment.Review, error) {
	p, _ := b.Plan(ctx)
	b.sequence++
	r := deployment.Review{ID: fmt.Sprintf("demo-review-%d", b.sequence), Kind: "xui-password-v1", Plan: p, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(5 * time.Minute), CanApply: p.Status == "proposed" && !b.blocked}
	b.review = &r
	return r, nil
}

func (b *preview) ApplyReview(_ context.Context, id string, confirmed bool) (deployment.OperationStatus, error) {
	if !confirmed || b.review == nil || b.review.ID != id || !b.review.CanApply || !time.Now().Before(b.review.ExpiresAt) {
		return deployment.OperationStatus{}, errors.New("preview requires an active confirmed review")
	}
	b.op = deployment.OperationStatus{ID: "demo-operation", ReviewID: id, Kind: "xui-password-v1", Status: "complete", Completed: 3, Total: 3}
	b.pending, b.review = false, nil
	return b.op, nil
}

func (b *preview) ResumeOperation(_ context.Context, id string) (deployment.OperationStatus, error) {
	if b.blocked || b.op.ID != id || b.op.Status != "ready" {
		return deployment.OperationStatus{}, errors.New("no resumable preview operation")
	}
	b.op.Status, b.op.Completed, b.pending = "complete", b.op.Total, false
	return b.op, nil
}

func (b *preview) CancelPreparation(_ context.Context, id string) (deployment.OperationStatus, error) {
	if b.blocked || b.op.ID != id || b.op.Status != "ready" || b.op.Completed != 0 {
		return deployment.OperationStatus{}, errors.New("no cancellable preview preparation")
	}
	b.op.Status = "cancelled"
	return b.op, nil
}

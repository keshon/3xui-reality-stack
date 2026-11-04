package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// This backend simulates screen transitions, not provisioning correctness.
// No filesystem, SSH, Docker or panel clients are available here. Password
// values are validated and discarded; only the pending-change flag is kept.
type preview struct {
	installation, initialized, deployed bool
	blocked                             bool
	pending                             bool
	sequence                            int
	review                              *deployment.Review
	op                                  deployment.OperationStatus
}

func newPreview(scenario string) (*preview, error) {
	b := &preview{op: deployment.OperationStatus{Status: "idle"}}
	switch scenario {
	case "installed":
		b.installation, b.initialized, b.deployed = true, true, true
		b.op = deployment.OperationStatus{ID: "demo-install-operation", Kind: "xui-install-v1", Status: "complete", Completed: 7, Total: 7}
	case "install", "install-blocked", "install-interrupted":
		b.installation = true
		b.blocked = scenario == "install-blocked"
		if scenario == "install-interrupted" {
			b.initialized, b.pending = true, true
			b.op = deployment.OperationStatus{ID: "demo-install-operation", Kind: "xui-install-v1", Status: "ready", Total: 7}
		}
	case "healthy":
	case "blocked":
		b.blocked = true
	case "interrupted":
		b.pending = true
		b.op = deployment.OperationStatus{ID: "demo-operation", Kind: "xui-password-v1", Status: "ready", Total: 3}
	default:
		return nil, errors.New("scenario must be healthy, blocked, interrupted, install, install-blocked, install-interrupted, or installed")
	}
	return b, nil
}

func (b *preview) Methods() []string {
	methods := []string{"inspect", "plan", "runtime", "operation", "review", "access"}
	if b.installation {
		methods = append(methods, "initialize", "preflight")
	}
	if !b.blocked || b.installation {
		methods = append(methods, "apply", "resume", "cancel-preparation")
		if !b.installation {
			methods = append(methods, "configure")
		}
	}
	return methods
}

func (b *preview) Access(context.Context) (deployment.AccessReport, error) {
	return deployment.AccessReport{
		Stack:        "3x-ui (mock)",
		Links:        []deployment.AccessValue{{Label: "Panel", Value: "https://ops.example.com/panel-example/"}, {Label: "Public website", Value: "https://www.example.com/"}},
		Credentials:  []deployment.AccessValue{{Label: "Admin username", Value: "admin"}, {Label: "Admin password", Value: "sample-password", Secret: true}},
		ConfigFile:   "/opt/new-stack/.env",
		Instructions: []string{"Open the full panel URL, including its generated path, and sign in.", "In the panel, open Clients to copy a user's subscription URL into HAPP.", "Mock data only; no host or network was contacted."},
	}, nil
}

func (b *preview) Inspect(context.Context) (deployment.Report, error) {
	r := deployment.Report{Stack: "3x-ui (mock)", Managed: !b.installation || b.initialized, Baseline: !b.installation || b.deployed}
	if b.blocked {
		r.Blockers = []string{"Mock: deployed panel version is outside the compatibility policy."}
	}
	return r, nil
}

func (b *preview) Plan(context.Context) (deployment.Plan, error) {
	p := deployment.Plan{Stack: "3x-ui (mock)", Status: "unchanged", ReadOnly: true}
	if b.installation && !b.deployed && !b.blocked {
		p.Status = "blocked"
		if b.initialized {
			p.Status = "proposed"
			p.Actions = []deployment.Action{{ID: "install", Resource: "Standalone stack", Effects: []string{"Create services, obtain certificates and provision three transports.", "Verify subscriptions and transfers; commit managed state.", "Mock only: nothing will be deployed."}}}
		}
		return p, nil
	}
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
	if b.installation {
		r.Kind = "xui-install-v1"
	}
	b.review = &r
	return r, nil
}

func (b *preview) ApplyReview(_ context.Context, id string, confirmed bool) (deployment.OperationStatus, error) {
	if !confirmed || b.review == nil || b.review.ID != id || !b.review.CanApply || !time.Now().Before(b.review.ExpiresAt) {
		return deployment.OperationStatus{}, errors.New("preview requires an active confirmed review")
	}
	b.op = deployment.OperationStatus{ID: "demo-operation", ReviewID: id, Kind: "xui-password-v1", Status: "complete", Completed: 3, Total: 3}
	b.pending, b.review = false, nil
	if b.installation {
		b.deployed = true
		b.op.Kind = "xui-install-v1"
		b.op.Completed, b.op.Total = 7, 7
	}
	return b.op, nil
}

func (b *preview) ResumeOperation(_ context.Context, id string) (deployment.OperationStatus, error) {
	if b.blocked || b.op.ID != id || b.op.Status != "ready" {
		return deployment.OperationStatus{}, errors.New("no resumable preview operation")
	}
	b.op.Status, b.op.Completed, b.pending = "complete", b.op.Total, false
	if b.installation {
		b.deployed = true
	}
	return b.op, nil
}

func (b *preview) CancelPreparation(_ context.Context, id string) (deployment.OperationStatus, error) {
	if b.blocked || b.op.ID != id || b.op.Status != "ready" || b.op.Completed != 0 {
		return deployment.OperationStatus{}, errors.New("no cancellable preview preparation")
	}
	b.op.Status = "cancelled"
	return b.op, nil
}

func (b *preview) InstallationTarget() string {
	if b.installation {
		return "/opt/mock-new-stack"
	}
	return ""
}
func (b *preview) Initialize(ctx context.Context, input json.RawMessage) (deployment.InitializationReport, error) {
	if !b.installation || b.initialized || !json.Valid(input) {
		return deployment.InitializationReport{}, errors.New("invalid mock initialization")
	}
	b.initialized, b.pending = true, true
	return deployment.InitializationReport{Stack: "3x-ui (mock)", Status: "prepared", Files: []string{".stack/config.json"}}, ctx.Err()
}
func (b *preview) Preflight(ctx context.Context) (deployment.PreflightReport, error) {
	state := "candidate"
	if b.blocked {
		state = "blocked"
	}
	installation := "absent"
	if b.initialized {
		installation = "draft"
	}
	return deployment.PreflightReport{Stack: "3x-ui (mock)", Status: state, Installation: installation, Checks: []deployment.Check{{ID: "existing-containers", OK: !b.blocked, Reason: "Mock host inventory; no real runtime is touched."}}}, ctx.Err()
}

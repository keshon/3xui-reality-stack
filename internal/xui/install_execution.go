package xui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// InstallStack enables the accepted standalone installation workflow only.
// Existing installations and password changes use separate adapters.
type InstallStack struct{ Stack }

var _ deployment.ExecutionAdapter = InstallStack{}

func (s InstallStack) ReviewState(ctx context.Context, root string) (deployment.ReviewSpec, error) {
	return s.Stack.reviewInstall(ctx, root)
}

func (s InstallStack) Inspect(ctx context.Context, root string) (deployment.Inspection, error) {
	i, err := s.Stack.Inspect(ctx, root)
	if err != nil {
		return i, err
	}
	if i.Report.Baseline {
		i.Report.Blockers = append(i.Report.Blockers, "installation workflow requires an unused initialization draft")
		i.Plan.Status = "blocked"
		i.Plan.Reasons = i.Report.Blockers
		return i, nil
	}
	if _, err := os.Lstat(filepath.Join(root, ".stack/go-initialization.json")); err != nil {
		return i, nil
	}
	spec, err := s.ReviewState(ctx, root)
	if err != nil {
		if ctx.Err() != nil {
			return i, ctx.Err()
		}
		i.Report.Blockers = []string{"fresh-install draft, host or installed controller is not ready; check preflight and release checksums"}
		i.Plan.Status = "blocked"
		i.Plan.Reasons = i.Report.Blockers
		return i, nil
	}
	i.Report.Blockers = []string{}
	i.Report.Observation = "fresh-install inputs and host checked; create a review before applying; no resources reserved"
	i.Plan = spec.Plan
	return i, nil
}

func (s InstallStack) OpenReviewed(ctx context.Context, root, kind string, raw json.RawMessage) (deployment.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if kind != "xui-install-v1" {
		return nil, errors.New("unsupported installation workflow")
	}
	binding, err := decodeInstallBinding(raw)
	if err != nil {
		return nil, err
	}
	return &installRuntime{root: root, stack: s.Stack, binding: &binding}, nil
}

package control

import (
	"context"
	"errors"
	"slices"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Preflighter interface {
	Preflight(context.Context) (deployment.PreflightReport, error)
}

type preflightAdapter interface {
	Preflight(context.Context, string) (deployment.PreflightReport, error)
}

func (c Controller) Preflight(ctx context.Context) (deployment.PreflightReport, error) {
	if err := ctx.Err(); err != nil {
		return deployment.PreflightReport{}, err
	}
	adapter, ok := c.Stack.(preflightAdapter)
	if !ok {
		return deployment.PreflightReport{}, errors.New("adapter does not support host preflight")
	}
	report, err := adapter.Preflight(ctx, c.Root)
	if err != nil {
		if ctx.Err() != nil {
			return deployment.PreflightReport{}, ctx.Err()
		}
		return deployment.PreflightReport{}, errors.New("host preflight failed")
	}
	return report, nil
}

func (c *Client) Preflight(ctx context.Context) (deployment.PreflightReport, error) {
	var report deployment.PreflightReport
	if !slices.Contains(c.methods, "preflight") {
		return report, errors.New("controller does not support host preflight")
	}
	err := c.call(ctx, "preflight", &report)
	if err == nil && (report.Stack == "" || !slices.Contains([]string{"candidate", "blocked"}, report.Status) || !slices.Contains([]string{"absent", "empty", "managed", "legacy", "unmanaged", "unknown"}, report.Installation)) {
		c.Close()
		return report, errors.New("incomplete controller preflight")
	}
	return report, err
}

// Package control is the UI-facing controller API and its private stdio protocol.
// It imports deployment contracts, not stack adapters or terminal libraries.
package control

import (
	"context"
	"errors"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// Inspector is implemented by stack adapters. Root is fixed by server startup,
// so a remote request cannot select an arbitrary directory on the host.
type Inspector interface {
	Inspect(context.Context, string) (deployment.Inspection, error)
	Runtime(context.Context, string) (deployment.RuntimeReport, error)
}

// Reader is shared by the local controller, protocol client and future TUI.
// Review and execution are separate capabilities behind Operator.
type Reader interface {
	Inspect(context.Context) (deployment.Report, error)
	Plan(context.Context) (deployment.Plan, error)
	Runtime(context.Context) (deployment.RuntimeReport, error)
}

type Controller struct {
	Root  string
	Stack Inspector
}

func (c Controller) snapshot(ctx context.Context) (deployment.Inspection, error) {
	if err := ctx.Err(); err != nil {
		return deployment.Inspection{}, err
	}
	if c.Stack == nil {
		return deployment.Inspection{}, errors.New("controller has no stack adapter")
	}
	i, err := c.Stack.Inspect(ctx, c.Root)
	if err != nil {
		if ctx.Err() != nil {
			return i, ctx.Err()
		}
		// Unknown adapter errors can quote secrets, paths or service responses.
		return deployment.Inspection{}, errors.New("stack inspection failed; check local state and compatibility")
	}
	return i, nil
}

func (c Controller) Inspect(ctx context.Context) (deployment.Report, error) {
	i, err := c.snapshot(ctx)
	return i.Report, err
}

func (c Controller) Plan(ctx context.Context) (deployment.Plan, error) {
	i, err := c.snapshot(ctx)
	return i.Plan, err
}

func (c Controller) Runtime(ctx context.Context) (deployment.RuntimeReport, error) {
	if err := ctx.Err(); err != nil {
		return deployment.RuntimeReport{}, err
	}
	if c.Stack == nil {
		return deployment.RuntimeReport{}, errors.New("controller has no stack adapter")
	}
	report, err := c.Stack.Runtime(ctx, c.Root)
	if err != nil {
		if ctx.Err() != nil {
			return deployment.RuntimeReport{}, ctx.Err()
		}
		return deployment.RuntimeReport{}, errors.New("runtime inspection failed")
	}
	return report, nil
}

package control

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Initializer interface {
	Initialize(context.Context, json.RawMessage) (deployment.InitializationReport, error)
}

type initializationAdapter interface {
	Initialize(context.Context, string, json.RawMessage) (deployment.InitializationReport, error)
}

func (c Controller) Initialize(ctx context.Context, input json.RawMessage) (deployment.InitializationReport, error) {
	if err := ctx.Err(); err != nil {
		return deployment.InitializationReport{}, err
	}
	adapter, ok := c.Stack.(initializationAdapter)
	if !ok {
		return deployment.InitializationReport{}, errors.New("adapter does not support initialization")
	}
	report, err := adapter.Initialize(ctx, c.Root, input)
	if err != nil {
		if ctx.Err() != nil {
			return deployment.InitializationReport{}, ctx.Err()
		}
		return deployment.InitializationReport{}, errors.New("initialization failed; check input, target ownership and existing draft state")
	}
	return report, nil
}

func (c *Client) Initialize(ctx context.Context, input json.RawMessage) (deployment.InitializationReport, error) {
	var report deployment.InitializationReport
	if !slices.Contains(c.methods, "initialize") {
		return report, errors.New("controller does not support initialization")
	}
	err := c.callParams(ctx, "initialize", initializationParams{Input: input}, &report)
	if err == nil && (report.Stack == "" || report.Status != "prepared" || report.CanApply || len(report.Files) == 0) {
		c.Close()
		return report, errors.New("incomplete controller initialization result")
	}
	return report, err
}

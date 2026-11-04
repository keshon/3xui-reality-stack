package control

import (
	"context"
	"errors"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// Configurator is separate from read-only inspection and operation execution.
// Adapters validate their own field names and save desired configuration only.
type Configurator interface {
	Configure(context.Context, map[string]string) (deployment.Plan, error)
}

type configAdapter interface {
	Configure(context.Context, string, map[string]string) (deployment.Plan, error)
}

func (c Controller) Configure(ctx context.Context, changes map[string]string) (deployment.Plan, error) {
	if err := ctx.Err(); err != nil {
		return deployment.Plan{}, err
	}
	adapter, ok := c.Stack.(configAdapter)
	if !ok {
		return deployment.Plan{}, deployment.ErrExecutionUnavailable
	}
	plan, err := adapter.Configure(ctx, c.Root, changes)
	if err != nil {
		if ctx.Err() != nil {
			return deployment.Plan{}, ctx.Err()
		}
		return deployment.Plan{}, errors.New("configuration edit failed; check managed state, pending operations and supported fields")
	}
	return plan, nil
}

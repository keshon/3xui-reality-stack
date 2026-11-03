package xui

import (
	"context"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

// Stack is the CLI's composition boundary. The state reader and workflow keep
// their richer private types within this adapter.
type Stack struct {
	docker        dockerCommand
	preflightHost func(context.Context, []deployment.Port) (host.Observation, error)
	installHost   func(context.Context, string, string) (installHostObservation, error)
}

func (Stack) Inspect(ctx context.Context, root string) (deployment.Inspection, error) {
	if err := ctx.Err(); err != nil {
		return deployment.Inspection{}, err
	}
	i, err := Inspect(root)
	if err != nil {
		return deployment.Inspection{}, err
	}
	return deployment.Inspection{Report: i.Report, Plan: i.Plan()}, nil
}

package control

import (
	"context"
	"errors"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Operator interface {
	Review(context.Context) (deployment.Review, error)
	Operation(context.Context) (deployment.OperationStatus, error)
	ApplyReview(context.Context, string, bool) (deployment.OperationStatus, error)
	ResumeOperation(context.Context, string) (deployment.OperationStatus, error)
	CancelPreparation(context.Context, string) (deployment.OperationStatus, error)
}

func (c Controller) Methods() []string {
	methods := []string{"inspect", "plan", "runtime", "operation"}
	if _, ok := c.Stack.(accessAdapter); ok {
		methods = append(methods, "access")
	}
	if _, ok := c.Stack.(initializationAdapter); ok {
		methods = append(methods, "initialize")
	}
	if _, ok := c.Stack.(preflightAdapter); ok {
		methods = append(methods, "preflight")
	}
	if _, ok := c.Stack.(deployment.ReviewAdapter); ok {
		methods = append(methods, "review")
	}
	if _, ok := c.Stack.(deployment.ExecutionAdapter); ok {
		methods = append(methods, "apply", "resume", "cancel-preparation")
	}
	if _, ok := c.Stack.(configAdapter); ok {
		methods = append(methods, "configure")
	}
	return methods
}

func (c Controller) Review(ctx context.Context) (deployment.Review, error) {
	adapter, ok := c.Stack.(deployment.ReviewAdapter)
	if !ok {
		return deployment.Review{}, errors.New("adapter does not support review")
	}
	return (deployment.Engine{Root: c.Root}).Review(ctx, adapter)
}

func (c Controller) Operation(ctx context.Context) (deployment.OperationStatus, error) {
	if err := ctx.Err(); err != nil {
		return deployment.OperationStatus{}, err
	}
	return (deployment.Engine{Root: c.Root}).Status()
}

func (c Controller) ApplyReview(ctx context.Context, id string, confirmed bool) (deployment.OperationStatus, error) {
	adapter, ok := c.Stack.(deployment.ExecutionAdapter)
	if !ok {
		return deployment.OperationStatus{}, deployment.ErrExecutionUnavailable
	}
	return (deployment.Engine{Root: c.Root}).ApplyReview(ctx, id, confirmed, adapter)
}

func (c Controller) ResumeOperation(ctx context.Context, id string) (deployment.OperationStatus, error) {
	adapter, ok := c.Stack.(deployment.ExecutionAdapter)
	if !ok {
		return deployment.OperationStatus{}, deployment.ErrExecutionUnavailable
	}
	return (deployment.Engine{Root: c.Root}).ResumeReviewed(ctx, id, adapter)
}

func (c Controller) CancelPreparation(ctx context.Context, id string) (deployment.OperationStatus, error) {
	if _, ok := c.Stack.(deployment.ExecutionAdapter); !ok {
		return deployment.OperationStatus{}, deployment.ErrExecutionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return deployment.OperationStatus{}, err
	}
	if !validPublicID(id) {
		return deployment.OperationStatus{}, errors.New("an exact operation identifier is required")
	}
	e := deployment.Engine{Root: c.Root}
	if err := e.CancelPreparationExpected(id); err != nil {
		return deployment.OperationStatus{}, err
	}
	return e.Status()
}

func validPublicID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

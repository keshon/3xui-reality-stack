package control

import (
	"context"
	"errors"
	"slices"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type AccessReader interface {
	Access(context.Context) (deployment.AccessReport, error)
}

type accessAdapter interface {
	Access(context.Context, string) (deployment.AccessReport, error)
}

func (c Controller) Access(ctx context.Context) (deployment.AccessReport, error) {
	if err := ctx.Err(); err != nil {
		return deployment.AccessReport{}, err
	}
	a, ok := c.Stack.(accessAdapter)
	if !ok {
		return deployment.AccessReport{}, errors.New("stack does not provide access details")
	}
	r, err := a.Access(ctx, c.Root)
	if err != nil {
		return deployment.AccessReport{}, errors.New("cannot read applied access details; inspect installation state")
	}
	return r, nil
}

func (c *Client) Access(ctx context.Context) (deployment.AccessReport, error) {
	var r deployment.AccessReport
	if !slices.Contains(c.methods, "access") {
		return r, errors.New("controller does not provide access details")
	}
	err := c.call(ctx, "access", &r)
	if err == nil && (r.Stack == "" || len(r.Links) == 0) {
		c.Close()
		return deployment.AccessReport{}, errors.New("incomplete controller access details")
	}
	return r, err
}

package control

import (
	"context"
	"errors"
	"slices"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func (c *Client) require(method string) error {
	if !slices.Contains(c.methods, method) {
		return errors.New("controller does not support the requested operation")
	}
	return nil
}

func (c *Client) Review(ctx context.Context) (deployment.Review, error) {
	var review deployment.Review
	if err := c.require("review"); err != nil {
		return review, err
	}
	err := c.call(ctx, "review", &review)
	if err == nil && (!validPublicID(review.ID) || review.Kind == "" || review.Plan.Status != "proposed" || !review.Plan.ReadOnly || !review.ExpiresAt.After(review.CreatedAt)) {
		c.Close()
		return review, errors.New("invalid controller review")
	}
	return review, err
}

func (c *Client) operationCall(ctx context.Context, method string, params any) (deployment.OperationStatus, error) {
	var status deployment.OperationStatus
	if err := c.require(method); err != nil {
		return status, err
	}
	err := c.callParams(ctx, method, params, &status)
	valid := slices.Contains([]string{"idle", "preparing", "ready", "running", "failed", "complete", "cancelled"}, status.Status)
	if status.Status != "idle" {
		valid = valid && validPublicID(status.ID) && status.Kind != ""
	}
	if err == nil && (!valid || status.Completed < 0 || status.Total < status.Completed) {
		c.Close()
		return status, errors.New("invalid controller operation status")
	}
	return status, err
}

func (c *Client) Operation(ctx context.Context) (deployment.OperationStatus, error) {
	return c.operationCall(ctx, "operation", nil)
}

func (c *Client) ApplyReview(ctx context.Context, id string, confirmed bool) (deployment.OperationStatus, error) {
	if !confirmed || !validPublicID(id) {
		return deployment.OperationStatus{}, errors.New("explicit confirmation of an exact review identifier is required")
	}
	return c.operationCall(ctx, "apply", applyParams{id, confirmed})
}

func (c *Client) ResumeOperation(ctx context.Context, id string) (deployment.OperationStatus, error) {
	if !validPublicID(id) {
		return deployment.OperationStatus{}, errors.New("an exact operation identifier is required")
	}
	return c.operationCall(ctx, "resume", operationParams{id})
}

func (c *Client) CancelPreparation(ctx context.Context, id string) (deployment.OperationStatus, error) {
	if !validPublicID(id) {
		return deployment.OperationStatus{}, errors.New("an exact operation identifier is required")
	}
	return c.operationCall(ctx, "cancel-preparation", operationParams{id})
}

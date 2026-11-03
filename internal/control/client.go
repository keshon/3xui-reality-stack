package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Client struct {
	stream    io.ReadWriteCloser
	in        *bufio.Reader
	gate      chan struct{}
	next      uint64
	methods   []string
	closed    atomic.Bool
	closeOnce sync.Once
}

// Connect negotiates capabilities before permitting requests. Cancellation
// closes the whole connection; callers reconnect and inspect rather than retry.
func Connect(ctx context.Context, stream io.ReadWriteCloser) (*Client, error) {
	c := &Client{stream: stream, in: bufio.NewReaderSize(stream, maxFrame+1), gate: make(chan struct{}, 1)}
	var hello capabilities
	if err := c.call(ctx, "hello", &hello); err != nil {
		c.Close()
		return nil, err
	}
	if hello.Version != ProtocolVersion || hello.ReadOnly == slices.Contains(hello.Methods, "apply") || !slices.Contains(hello.Methods, "inspect") || !slices.Contains(hello.Methods, "plan") {
		c.Close()
		return nil, errors.New("controller capabilities are incompatible")
	}
	c.methods = append([]string(nil), hello.Methods...)
	return c, nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.closed.Store(true); _ = c.stream.Close() })
	return nil
}

func (c *Client) call(ctx context.Context, method string, target any) error {
	return c.callParams(ctx, method, nil, target)
}

func (c *Client) callParams(ctx context.Context, method string, params any, target any) error {
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.gate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed.Load() {
		return errors.New("controller connection is closed")
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(finished) })
	defer func() {
		if !stop() {
			<-finished
		}
	}()
	c.next++
	req := request{Version: ProtocolVersion, ID: c.next, Method: method}
	if params != nil {
		var err error
		req.Params, err = json.Marshal(params)
		if err != nil {
			return errors.New("invalid controller parameters")
		}
	}
	if err := writeFrame(c.stream, req); err != nil {
		c.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("controller connection interrupted")
	}
	data, err := readFrame(c.in)
	if err != nil {
		c.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("controller response interrupted or exceeded frame limits")
	}
	var reply response
	if decode(data, &reply) != nil || reply.Version != ProtocolVersion || reply.ID != c.next || (reply.Error != "" && len(reply.Result) != 0) {
		c.Close()
		return errors.New("controller response does not match the request")
	}
	if reply.Error != "" {
		switch reply.Error {
		case "unsupported_protocol":
			return errors.New("controller protocol version is incompatible")
		case "unsupported_method":
			return errors.New("controller does not support the requested operation")
		case "invalid_params":
			return errors.New("controller rejected operation parameters or confirmation")
		case "operation_failed":
			return errors.New("controller operation did not complete; inspect operation status before retrying")
		case "configuration_failed":
			return errors.New("controller could not save desired configuration; check pending operations, drift and supported fields")
		case "initialization_failed":
			return errors.New("controller could not prepare initialization; check input and existing target state")
		case "stale_review":
			return deployment.ErrStaleReview
		case "expired_review":
			return deployment.ErrExpiredReview
		case "consumed_review":
			return deployment.ErrConsumedReview
		case "execution_unavailable":
			return deployment.ErrExecutionUnavailable
		default:
			return errors.New("controller could not complete the request")
		}
	}
	if len(reply.Result) == 0 || bytes.Equal(bytes.TrimSpace(reply.Result), []byte("null")) || json.Unmarshal(reply.Result, target) != nil {
		c.Close()
		return errors.New("invalid controller result")
	}
	return nil
}

func (c *Client) Inspect(ctx context.Context) (deployment.Report, error) {
	var report deployment.Report
	err := c.call(ctx, "inspect", &report)
	if err == nil && report.Stack == "" {
		c.Close()
		return report, errors.New("incomplete controller inspection")
	}
	return report, err
}

func (c *Client) Plan(ctx context.Context) (deployment.Plan, error) {
	var plan deployment.Plan
	err := c.call(ctx, "plan", &plan)
	if err == nil && (plan.Stack == "" || !plan.ReadOnly || !slices.Contains([]string{"unchanged", "proposed", "blocked"}, plan.Status)) {
		c.Close()
		return plan, errors.New("incomplete controller plan")
	}
	return plan, err
}

func (c *Client) Configure(ctx context.Context, changes map[string]string) (deployment.Plan, error) {
	var plan deployment.Plan
	if !slices.Contains(c.methods, "configure") {
		return plan, deployment.ErrExecutionUnavailable
	}
	err := c.callParams(ctx, "configure", configureParams{Changes: changes}, &plan)
	if err == nil && (plan.Stack == "" || !plan.ReadOnly || !slices.Contains([]string{"unchanged", "proposed"}, plan.Status)) {
		c.Close()
		return plan, errors.New("incomplete controller configuration result")
	}
	return plan, err
}

func (c *Client) Runtime(ctx context.Context) (deployment.RuntimeReport, error) {
	var report deployment.RuntimeReport
	if !slices.Contains(c.methods, "runtime") {
		return report, errors.New("controller does not support runtime discovery")
	}
	err := c.call(ctx, "runtime", &report)
	if err == nil && (report.Stack == "" || !slices.Contains([]string{"compatible", "blocked"}, report.Status)) {
		c.Close()
		return report, errors.New("incomplete controller runtime report")
	}
	return report, err
}

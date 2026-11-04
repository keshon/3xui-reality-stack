package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Options struct {
	Confirm     string
	OperationID string
}

func exactID(value string) bool {
	return len(value) == 32 && strings.Trim(value, "0123456789abcdef") == ""
}

func (o Options) Validate(method string) error {
	switch method {
	case "inspect", "plan", "runtime", "preflight", "review", "operation":
		if o.Confirm == "" && o.OperationID == "" {
			return nil
		}
	case "apply":
		if exactID(o.Confirm) && o.OperationID == "" {
			return nil
		}
	case "resume", "cancel-preparation":
		if o.Confirm == "" && exactID(o.OperationID) {
			return nil
		}
	}
	return errors.New("invalid command or identifiers; apply requires --confirm REVIEW_ID; recovery requires --operation OPERATION_ID")
}

func Run(ctx context.Context, method string, options Options, out io.Writer, reader control.Reader) error {
	if err := options.Validate(method); err != nil {
		return err
	}
	if method == "inspect" || method == "plan" || method == "runtime" || method == "preflight" {
		return Read(ctx, method, out, reader)
	}
	operator, ok := reader.(control.Operator)
	if !ok {
		return errors.New("controller does not support operation management")
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	if method == "review" {
		review, err := operator.Review(ctx)
		if err != nil {
			return err
		}
		return e.Encode(review)
	}
	var status deployment.OperationStatus
	var err error
	switch method {
	case "operation":
		status, err = operator.Operation(ctx)
	case "apply":
		status, err = operator.ApplyReview(ctx, options.Confirm, true)
	case "resume":
		status, err = operator.ResumeOperation(ctx, options.OperationID)
	case "cancel-preparation":
		status, err = operator.CancelPreparation(ctx, options.OperationID)
	}
	if err != nil {
		return err
	}
	if err := e.Encode(status); err != nil {
		return err
	}
	if (method == "apply" || method == "resume") && status.Status != "complete" {
		return errors.New("operation needs attention; inspect status before recovery")
	}
	return nil
}

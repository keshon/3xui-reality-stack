// Package console renders commands shared by the local and remote CLI.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/keshon/3xui-reality-stack/internal/control"
)

func Read(ctx context.Context, method string, out io.Writer, reader control.Reader) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	switch method {
	case "preflight":
		preflight, ok := reader.(control.Preflighter)
		if !ok {
			return errors.New("controller does not support host preflight")
		}
		report, err := preflight.Preflight(ctx)
		if err != nil {
			return err
		}
		if err := e.Encode(report); err != nil {
			return err
		}
		if report.Status == "blocked" {
			return errors.New("fresh deployment preflight blocked; see checks above")
		}
		return nil
	case "inspect":
		report, err := reader.Inspect(ctx)
		if err != nil {
			return err
		}
		return e.Encode(report)
	case "plan":
		plan, err := reader.Plan(ctx)
		if err != nil {
			return err
		}
		if err := e.Encode(plan); err != nil {
			return err
		}
		if plan.Status == "blocked" {
			return errors.New("plan blocked; see reasons above")
		}
		return nil
	case "runtime":
		report, err := reader.Runtime(ctx)
		if err != nil {
			return err
		}
		if err := e.Encode(report); err != nil {
			return err
		}
		if report.Status == "blocked" {
			return errors.New("runtime compatibility blocked; see checks above")
		}
		return nil
	default:
		return errors.New("unsupported read-only command")
	}
}

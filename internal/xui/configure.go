package xui

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"unicode/utf8"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// Configure edits desired state only. Runtime effects require a separate bound
// review and explicit apply. This first slice accepts only PANEL_PASS.
func (s PasswordStack) Configure(ctx context.Context, root string, changes map[string]string) (deployment.Plan, error) {
	if len(changes) != 1 {
		return deployment.Plan{}, errors.New("only an isolated panel password edit is supported")
	}
	password, ok := changes["PANEL_PASS"]
	if !ok || password == "" || len(password) > 4096 || !utf8.ValidString(password) || controls(password) {
		return deployment.Plan{}, errors.New("panel password must be valid UTF-8, contain no control characters and fit within 4096 bytes")
	}
	if err := ctx.Err(); err != nil {
		return deployment.Plan{}, err
	}
	unlock, err := deployment.Lock(root)
	if err != nil {
		return deployment.Plan{}, err
	}
	defer unlock()
	if err := (deployment.Engine{Root: root}).CheckIdle(); err != nil {
		return deployment.Plan{}, err
	}
	before, err := readPasswordInputs(root)
	if err != nil {
		return deployment.Plan{}, err
	}
	i, err := Inspect(root)
	if err != nil {
		return deployment.Plan{}, err
	}
	if plan := i.Plan(); plan.Status != "unchanged" && plan.Status != "proposed" {
		return deployment.Plan{}, errors.New("configuration is blocked; resolve drift or unrelated edits first")
	}
	unchanged := i.desired.Env["PANEL_PASS"] == password
	i.desired.Env["PANEL_PASS"] = password
	plan := i.Plan()
	if plan.Status != "unchanged" && plan.Status != "proposed" {
		return deployment.Plan{}, errors.New("password edit is unsupported for this installation")
	}
	after, err := readPasswordInputs(root)
	if err != nil {
		return deployment.Plan{}, err
	}
	for _, name := range passwordInputs {
		if !bytes.Equal(before[name], after[name]) {
			return deployment.Plan{}, errors.New("managed inputs changed during configuration edit")
		}
	}
	if err := ctx.Err(); err != nil {
		return deployment.Plan{}, err
	}
	if !unchanged {
		if err := deployment.WriteJSON(filepath.Join(root, ".stack", "config.json"), i.desired); err != nil {
			return deployment.Plan{}, err
		}
	}
	return plan, nil
}

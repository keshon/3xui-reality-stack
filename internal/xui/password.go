package xui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

var passwordInputs = []string{".stack/config.json", ".stack/applied.json", ".env", "users.txt", "nodes.txt"}

// PasswordChange is an experimental workflow, not yet exposed by the CLI.
// A reviewed instance binds preparation and writes to local inputs and runtime.
type PasswordChange struct {
	root    string
	panel   *Panel
	stack   Stack
	binding *passwordBinding
}

type passwordReview struct {
	Endpoint string          `json:"endpoint"`
	Binding  passwordBinding `json:"binding"`
}

type passwordSnapshot struct {
	Version      int               `json:"version"`
	Endpoint     string            `json:"endpoint"`
	Inputs       map[string][]byte `json:"inputs"`
	Targets      map[string][]byte `json:"targets"`
	Preserved    []byte            `json:"preserved"`
	DatabaseHash string            `json:"database_hash"`
}

func ReviewPassword(root string, panel *Panel) (*PasswordChange, error) {
	return (Stack{}).reviewPassword(context.Background(), root, panel)
}

func (s Stack) reviewPassword(ctx context.Context, root string, panel *Panel) (*PasswordChange, error) {
	if panel == nil {
		return nil, errors.New("panel endpoint is required")
	}
	spec, err := s.ReviewState(ctx, root)
	if err != nil {
		return nil, err
	}
	return s.openPassword(root, panel, spec.Binding)
}

// openPassword consumes the original private review, including during recovery.
// It does not refresh the observation or expose execution through Stack.
func (s Stack) openPassword(root string, panel *Panel, raw json.RawMessage) (*PasswordChange, error) {
	binding, err := decodePasswordBinding(raw)
	if err != nil || panel == nil {
		return nil, errors.New("invalid password review binding")
	}
	return &PasswordChange{root: root, panel: panel, stack: s, binding: &binding}, nil
}

func decodePasswordBinding(raw json.RawMessage) (passwordBinding, error) {
	var binding passwordBinding
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !json.Valid(raw) || d.Decode(&binding) != nil || len(binding.Inputs) != len(passwordInputs) || len(binding.Policy) == 0 || !dockerID(binding.Runtime.Panel.ID) || !dockerID(binding.Runtime.Traefik.ID) {
		return binding, errors.New("invalid password review binding")
	}
	for _, name := range passwordInputs {
		if binding.Inputs[name] == nil {
			return binding, errors.New("incomplete password review inputs")
		}
	}
	return binding, nil
}

// ResumePassword opens only the immutable operation snapshot, never a new plan.
func ResumePassword(root string, panel *Panel) *PasswordChange {
	return &PasswordChange{root: root, panel: panel}
}
func (*PasswordChange) Kind() string             { return "xui-password-v1" }
func (w *PasswordChange) DeploymentRoot() string { return w.root }

func readPasswordInputs(root string) (map[string][]byte, error) {
	result := map[string][]byte{}
	for _, name := range passwordInputs {
		path := filepath.Join(root, name)
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("password change requires regular managed input files")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("cannot read password operation inputs")
		}
		result[name] = data
	}
	return result, nil
}

func (w *PasswordChange) Prepare(ctx context.Context, dir string) error {
	if err := w.loadReview(dir); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err == nil {
		_, err := w.open(dir)
		return err
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect preparation state")
	}
	current, err := readPasswordInputs(w.root)
	if err != nil {
		return err
	}
	for _, name := range passwordInputs {
		if !bytes.Equal(current[name], w.binding.Inputs[name]) {
			return errors.New("reviewed inputs changed")
		}
	}
	i, err := inspect(w.root, true)
	if err != nil {
		return err
	}
	if i.Plan().Status != "proposed" {
		return errors.New("password plan is no longer applicable")
	}
	if err := w.checkRuntime(ctx); err != nil {
		return err
	}
	env, err := replacePassword(current[".env"], i.applied.Env["PANEL_PASS"], i.desired.Env["PANEL_PASS"])
	if err != nil {
		return err
	}
	s, err := w.panel.login(ctx, i.applied.Env["PANEL_USER"], i.applied.Env["PANEL_PASS"])
	if err != nil {
		return err
	}
	preserved, err := s.preservation(ctx)
	if err != nil {
		return err
	}
	database, err := s.database(ctx)
	if err != nil {
		return err
	}
	after, err := s.preservation(ctx)
	if err != nil || !bytes.Equal(after, preserved) {
		return errors.New("panel configuration changed during preparation")
	}
	if err := w.checkRuntime(ctx); err != nil {
		return err
	}
	targets, err := passwordTargets(current, env, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(database)
	snapshot := passwordSnapshot{Version: 1, Endpoint: w.panel.base, Inputs: current, Targets: targets, Preserved: preserved, DatabaseHash: hex.EncodeToString(sum[:])}
	if err := deployment.WriteFile(filepath.Join(dir, "panel.db"), database); err != nil {
		return err
	}
	// state.json is the preparation completion marker; it is written last.
	return deployment.WriteJSON(filepath.Join(dir, "state.json"), snapshot)
}

func (w *PasswordChange) open(dir string) (passwordSnapshot, error) {
	var s passwordSnapshot
	if err := w.loadReview(dir); err != nil {
		return s, err
	}
	exists, err := readJSON(filepath.Join(dir, "state.json"), &s, true)
	if err != nil || !exists || s.Version != 1 || w.panel == nil || s.Endpoint != w.panel.base || len(s.Inputs) != len(passwordInputs) || len(s.Targets) != 2 || len(s.Preserved) == 0 {
		return s, errors.New("invalid password recovery snapshot")
	}
	for _, name := range passwordInputs {
		if s.Inputs[name] == nil || !bytes.Equal(s.Inputs[name], w.binding.Inputs[name]) {
			return s, errors.New("incomplete password recovery inputs")
		}
	}
	if s.Targets[".env"] == nil || s.Targets[".stack/applied.json"] == nil {
		return s, errors.New("incomplete password recovery targets")
	}
	var desired config
	var previous baseline
	var target struct {
		AppliedAt string `json:"applied_at"`
	}
	if json.Unmarshal(s.Inputs[".stack/config.json"], &desired) != nil || json.Unmarshal(s.Inputs[".stack/applied.json"], &previous) != nil || json.Unmarshal(s.Targets[".stack/applied.json"], &target) != nil {
		return s, errors.New("invalid recovery configuration")
	}
	i := Inspection{Report: deployment.Report{Managed: true, Baseline: true}, desired: desired, applied: previous.Config}
	if !valid(desired) || !valid(previous.Config) || i.Plan().Status != "proposed" {
		return s, errors.New("recovery no longer describes an isolated password change")
	}
	env, err := replacePassword(s.Inputs[".env"], previous.Config.Env["PANEL_PASS"], desired.Env["PANEL_PASS"])
	if err != nil {
		return s, err
	}
	expected, err := passwordTargets(s.Inputs, env, target.AppliedAt)
	if err != nil || !bytes.Equal(expected[".env"], s.Targets[".env"]) || !bytes.Equal(expected[".stack/applied.json"], s.Targets[".stack/applied.json"]) {
		return s, errors.New("recovery targets differ from frozen inputs")
	}
	db, err := os.ReadFile(filepath.Join(dir, "panel.db"))
	if err != nil {
		return s, errors.New("password recovery database is missing")
	}
	sum := sha256.Sum256(db)
	if hex.EncodeToString(sum[:]) != s.DatabaseHash {
		return s, errors.New("password recovery database changed")
	}
	return s, w.checkLocal(s)
}

// The original review is persisted before preparation's first API call. Recovery
// never substitutes a fresh observation for that review.
func (w *PasswordChange) loadReview(dir string) error {
	if w.panel == nil {
		return errors.New("panel endpoint is required")
	}
	var saved passwordReview
	exists, err := readJSON(filepath.Join(dir, "reviewed.json"), &saved, true)
	if err != nil {
		return err
	}
	if !exists {
		if w.binding == nil {
			return errors.New("preparation did not freeze a runtime review")
		}
		return deployment.WriteJSON(filepath.Join(dir, "reviewed.json"), passwordReview{w.panel.base, *w.binding})
	}
	if saved.Endpoint != w.panel.base || len(saved.Binding.Inputs) != len(passwordInputs) || len(saved.Binding.Policy) == 0 || !dockerID(saved.Binding.Runtime.Panel.ID) || !dockerID(saved.Binding.Runtime.Traefik.ID) || (w.binding != nil && !reflect.DeepEqual(*w.binding, saved.Binding)) {
		return errors.New("password recovery review differs from the original binding")
	}
	w.binding = &saved.Binding
	return nil
}

func (w *PasswordChange) checkRuntime(ctx context.Context) error {
	if w.binding == nil {
		return errors.New("password operation has no runtime binding")
	}
	policy, err := os.ReadFile(filepath.Join(w.root, "compatibility.json"))
	if err != nil || !bytes.Equal(policy, w.binding.Policy) {
		return errors.New("compatibility policy differs from the reviewed operation")
	}
	var original baseline
	if json.Unmarshal(w.binding.Inputs[".stack/applied.json"], &original) != nil || !valid(original.Config) {
		return errors.New("invalid reviewed baseline")
	}
	report, observed, err := w.stack.observeAppliedRuntime(ctx, w.root, original.Config)
	if err != nil || report.Status != "compatible" || !reflect.DeepEqual(observed, w.binding.Runtime) {
		return errors.New("runtime differs from the reviewed operation")
	}
	// Catch edits made while the Docker commands were in flight.
	policy, err = os.ReadFile(filepath.Join(w.root, "compatibility.json"))
	if err != nil || !bytes.Equal(policy, w.binding.Policy) {
		return errors.New("compatibility policy changed during runtime inspection")
	}
	return ctx.Err()
}

func (w *PasswordChange) checkWrite(ctx context.Context, snapshot passwordSnapshot) error {
	if err := w.checkLocal(snapshot); err != nil {
		return err
	}
	if err := w.checkRuntime(ctx); err != nil {
		return err
	}
	return w.checkLocal(snapshot)
}

func passwordTargets(inputs map[string][]byte, env []byte, appliedAt string) (map[string][]byte, error) {
	if _, err := time.Parse(time.RFC3339, appliedAt); err != nil {
		return nil, errors.New("invalid recovery timestamp")
	}
	var applied map[string]json.RawMessage
	if json.Unmarshal(inputs[".stack/applied.json"], &applied) != nil || applied == nil {
		return nil, errors.New("invalid baseline")
	}
	var hashes map[string]*string
	if json.Unmarshal(applied["output_hashes"], &hashes) != nil || len(hashes) != 3 {
		return nil, errors.New("invalid output baseline")
	}
	digest := sha256.Sum256(env)
	hash := hex.EncodeToString(digest[:])
	hashes[".env"] = &hash
	applied["output_hashes"], _ = json.Marshal(hashes)
	applied["config"] = append([]byte(nil), inputs[".stack/config.json"]...)
	applied["applied_at"], _ = json.Marshal(appliedAt)
	appliedBytes, err := json.MarshalIndent(applied, "", "  ")
	if err != nil {
		return nil, errors.New("cannot encode new baseline")
	}
	return map[string][]byte{".env": env, ".stack/applied.json": append(appliedBytes, '\n')}, nil
}

func (w *PasswordChange) checkLocal(s passwordSnapshot) error {
	current, err := readPasswordInputs(w.root)
	if err != nil {
		return err
	}
	for _, name := range passwordInputs {
		if !bytes.Equal(current[name], s.Inputs[name]) && (s.Targets[name] == nil || !bytes.Equal(current[name], s.Targets[name])) {
			return errors.New("local inputs changed outside this operation")
		}
	}
	return nil
}

func (w *PasswordChange) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	snapshot, err := w.open(dir)
	if err != nil {
		return nil, err
	}
	var desired config
	var previous baseline
	if json.Unmarshal(snapshot.Inputs[".stack/config.json"], &desired) != nil || json.Unmarshal(snapshot.Inputs[".stack/applied.json"], &previous) != nil || !valid(desired) || !valid(previous.Config) {
		return nil, errors.New("invalid password recovery configuration")
	}
	user, old, password := desired.Env["PANEL_USER"], previous.Config.Env["PANEL_PASS"], desired.Env["PANEL_PASS"]
	verify := func(ctx context.Context) error {
		if err := w.checkWrite(ctx, snapshot); err != nil {
			return err
		}
		s, err := w.panel.login(ctx, user, password)
		if err != nil {
			return err
		}
		data, err := s.preservation(ctx)
		if err != nil || !bytes.Equal(data, snapshot.Preserved) {
			return errors.New("panel identities or settings differ from recovery point")
		}
		return nil
	}
	return []deployment.Step{
		{ID: "panel-password", Run: func(ctx context.Context) error {
			if err := w.checkWrite(ctx, snapshot); err != nil {
				return err
			}
			s, err := w.panel.login(ctx, user, old)
			if errors.Is(err, errCredentials) {
				// A previous request may have committed before the reply/checkpoint.
				return verify(ctx)
			}
			if err != nil {
				return err
			}
			data, err := s.preservation(ctx)
			if err != nil || !bytes.Equal(data, snapshot.Preserved) {
				return errors.New("panel changed before password mutation")
			}
			// Even a successful API response is not proof. A fresh login resolves
			// both successful and ambiguous outcomes without repeating a write.
			if err := w.checkWrite(ctx, snapshot); err != nil {
				return err
			}
			_ = s.changePassword(ctx, user, old, password)
			return verify(ctx)
		}},
		{ID: "verify", Run: verify},
		{ID: "commit", Run: func(ctx context.Context) error {
			// Re-verify on replay, including a crash between these two renames.
			if err := verify(ctx); err != nil {
				return err
			}
			for _, name := range []string{".env", ".stack/applied.json"} {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := w.checkWrite(ctx, snapshot); err != nil {
					return err
				}
				if err := deployment.WriteFile(filepath.Join(w.root, name), snapshot.Targets[name]); err != nil {
					return err
				}
			}
			return nil
		}},
	}, nil
}

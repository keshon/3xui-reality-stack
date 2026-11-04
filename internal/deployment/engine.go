package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
)

// Workflow owns stack-specific preconditions and recovery. Prepare may only
// inspect remote state and write local recovery artifacts. Each Step must
// tolerate replay after its effects succeeded but its checkpoint did not.
type Workflow interface {
	Kind() string
	DeploymentRoot() string
	Prepare(context.Context, string) error
	Steps(context.Context, string) ([]Step, error)
}

type Step struct {
	ID  string
	Run func(context.Context) error
}

type journal struct {
	Version  int      `json:"version"`
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Status   string   `json:"status"`
	Steps    []string `json:"steps"`
	Next     int      `json:"next"`
	ReviewID string   `json:"review_id,omitempty"`
}

// Engine persists intent before actions and checkpoints afterward. It neither
// knows stack APIs nor claims transactional rollback of remote state.
type Engine struct{ Root string }

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var operationID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (e Engine) Start(ctx context.Context, w Workflow) error  { return e.run(ctx, w, false) }
func (e Engine) Resume(ctx context.Context, w Workflow) error { return e.run(ctx, w, true) }

// CancelPreparation permits a new review only before any workflow step starts.
// Recovery artifacts stay on disk. It is not rollback of an attempted action.
func (e Engine) CancelPreparation() error {
	return e.CancelPreparationExpected("")
}

func (e Engine) CancelPreparationExpected(id string) error {
	unlock, err := Lock(e.Root)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(e.Root, ".stack", "go-operation.json")
	data, err := os.ReadFile(path)
	var j journal
	if err != nil || json.Unmarshal(data, &j) != nil || !validJournal(j) || (j.Status != "preparing" && !(j.Status == "ready" && j.Next == 0)) {
		return errors.New("only an unstarted preparation can be cancelled")
	}
	if id != "" && (!operationID.MatchString(id) || j.ID != id) {
		return errors.New("preparation no longer matches the requested operation")
	}
	j.Status = "cancelled"
	return WriteJSON(path, j)
}

func (e Engine) run(ctx context.Context, w Workflow, resume bool) error {
	if err := e.validateWorkflow(w); err != nil {
		return err
	}
	unlock, err := Lock(e.Root)
	if err != nil {
		return err
	}
	defer unlock()
	return e.runLocked(ctx, w, resume, "", "")
}

func (e Engine) validateWorkflow(w Workflow) error {
	if w == nil {
		return errors.New("workflow is unavailable")
	}
	if !identifier.MatchString(w.Kind()) {
		return errors.New("invalid workflow kind")
	}
	root, err := filepath.Abs(e.Root)
	workflowRoot, rootErr := filepath.Abs(w.DeploymentRoot())
	if err != nil || rootErr != nil || root != workflowRoot {
		return errors.New("workflow belongs to another deployment")
	}
	return nil
}

func (e Engine) runLocked(ctx context.Context, w Workflow, resume bool, startID, reviewID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state := filepath.Join(e.Root, ".stack")
	if err := legacyIdle(state); err != nil {
		return err
	}
	path := filepath.Join(state, "go-operation.json")
	var j journal
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot read Go operation")
	}
	if exists && (json.Unmarshal(data, &j) != nil || !validJournal(j)) {
		return errors.New("invalid Go operation; preserve it for recovery")
	}
	if resume {
		if !exists || j.Kind != w.Kind() {
			return errors.New("no matching Go operation to resume")
		}
		if j.Status == "complete" {
			return nil
		}
		if j.Status == "cancelled" {
			return errors.New("preparation was cancelled; review a new operation")
		}
	} else {
		if exists && j.Status != "complete" && j.Status != "cancelled" {
			return errors.New("a Go operation needs recovery")
		}
		if startID == "" {
			startID, err = newID()
			if err != nil {
				return err
			}
		}
		j = journal{Version: 1, ID: startID, ReviewID: reviewID, Kind: w.Kind(), Status: "preparing", Steps: []string{}}
		if err := WriteJSON(path, j); err != nil {
			return err
		}
	}
	operations := filepath.Join(state, "go-operations")
	if err := privateDirectory(operations); err != nil {
		return err
	}
	dir := filepath.Join(operations, j.ID)
	if err := privateDirectory(dir); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.Status == "preparing" {
		if err := w.Prepare(ctx, dir); err != nil {
			return errors.New("preparation stopped; no workflow mutation was started; resume after resolving the cause")
		}
	}
	steps, err := w.Steps(ctx, dir)
	if err != nil {
		return errors.New("cannot open workflow recovery state")
	}
	ids := make([]string, len(steps))
	seen := map[string]bool{}
	for n, step := range steps {
		if !identifier.MatchString(step.ID) || seen[step.ID] || step.Run == nil {
			return errors.New("invalid workflow steps")
		}
		ids[n], seen[step.ID] = step.ID, true
	}
	if j.Status == "preparing" {
		j.Steps, j.Status = ids, "ready"
		if err := WriteJSON(path, j); err != nil {
			return err
		}
	} else if !reflect.DeepEqual(ids, j.Steps) {
		return errors.New("workflow changed; resume with the original adapter version")
	}
	for j.Next < len(steps) {
		if err := ctx.Err(); err != nil {
			return err
		}
		j.Status = "running"
		if err := WriteJSON(path, j); err != nil {
			return err
		}
		if err := steps[j.Next].Run(ctx); err != nil {
			j.Status = "failed"
			if saveErr := WriteJSON(path, j); saveErr != nil {
				return saveErr
			}
			// Adapter errors and service responses can contain credentials.
			return errors.New("operation stopped at " + ids[j.Next] + "; resume after resolving the cause")
		}
		j.Next++
		j.Status = "ready"
		if err := WriteJSON(path, j); err != nil {
			return err
		}
	}
	j.Status = "complete"
	return WriteJSON(path, j)
}

func validJournal(j journal) bool {
	if j.Version != 1 || !operationID.MatchString(j.ID) || !identifier.MatchString(j.Kind) || (j.ReviewID != "" && !operationID.MatchString(j.ReviewID)) || j.Next < 0 || j.Next > len(j.Steps) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range j.Steps {
		if !identifier.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	switch j.Status {
	case "preparing":
		return j.Next == 0 && len(j.Steps) == 0
	case "cancelled":
		return j.Next == 0
	case "ready":
		return true
	case "running", "failed":
		return j.Next < len(j.Steps)
	case "complete":
		return j.Next == len(j.Steps)
	default:
		return false
	}
}

func legacyIdle(state string) error {
	for _, name := range []string{"operation.json", "adoption.json", "upgrade.json"} {
		data, err := os.ReadFile(filepath.Join(state, name))
		if os.IsNotExist(err) {
			continue
		}
		var marker struct {
			Status string `json:"status"`
		}
		if err != nil || json.Unmarshal(data, &marker) != nil {
			return errors.New("cannot read legacy operation state")
		}
		if marker.Status != "complete" && !(name == "upgrade.json" && marker.Status == "rolled_back") {
			return errors.New("legacy controller needs recovery first")
		}
	}
	return nil
}

package deployment

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ReviewSpec separates public effects from the adapter's private observation.
// Binding must describe the exact inputs and runtime identity being reviewed.
type ReviewSpec struct {
	Kind    string
	Plan    Plan
	Binding json.RawMessage
}

type ReviewAdapter interface {
	ReviewState(context.Context, string) (ReviewSpec, error)
}

var (
	ErrStaleReview          = errors.New("review is stale; create a new review for the changed configuration or runtime")
	ErrExpiredReview        = errors.New("review expired; create a new review")
	ErrConsumedReview       = errors.New("review was already consumed; inspect operation state before creating a new review")
	ErrExecutionUnavailable = errors.New("adapter execution is unavailable pending integration validation")
)

// ExecutionAdapter is deliberately separate. An adapter may support inspection
// and review while its write path is still awaiting integration validation.
// The resulting workflow must recheck its binding before effects and on replay.
type ExecutionAdapter interface {
	ReviewAdapter
	OpenReviewed(context.Context, string, string, json.RawMessage) (Workflow, error)
}

type Review struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Plan      Plan      `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	CanApply  bool      `json:"can_apply"`
}

type reviewRecord struct {
	Version int             `json:"version"`
	Root    string          `json:"root"`
	Review  Review          `json:"review"`
	Binding json.RawMessage `json:"binding"`
	UsedBy  string          `json:"used_by,omitempty"`
}

func newID() (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", errors.New("cannot create operation identifier")
	}
	return hex.EncodeToString(id), nil
}

func canonicalBinding(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !json.Valid(raw) || d.Decode(&value) != nil || value == nil {
		return nil, errors.New("invalid private review binding")
	}
	return json.Marshal(value)
}

func validateSpec(spec ReviewSpec) (ReviewSpec, error) {
	if !identifier.MatchString(spec.Kind) || spec.Plan.Stack == "" || spec.Plan.Status != "proposed" || !spec.Plan.ReadOnly || len(spec.Plan.Actions) == 0 {
		return spec, errors.New("operation has no applicable review plan")
	}
	seen := map[string]bool{}
	for _, action := range spec.Plan.Actions {
		if !identifier.MatchString(action.ID) || seen[action.ID] {
			return spec, errors.New("invalid review action identifiers")
		}
		seen[action.ID] = true
	}
	var err error
	spec.Binding, err = canonicalBinding(spec.Binding)
	return spec, err
}

func (e Engine) Review(ctx context.Context, adapter ReviewAdapter) (Review, error) {
	unlock, err := Lock(e.Root)
	if err != nil {
		return Review{}, err
	}
	defer unlock()
	if err := e.CheckIdle(); err != nil {
		return Review{}, err
	}
	if err := ctx.Err(); err != nil {
		return Review{}, err
	}
	spec, err := adapter.ReviewState(ctx, e.Root)
	if err != nil {
		return Review{}, errors.New("cannot inspect inputs and runtime for review")
	}
	spec, err = validateSpec(spec)
	if err != nil {
		return Review{}, err
	}
	root, err := filepath.EvalSymlinks(e.Root)
	if err != nil {
		return Review{}, errors.New("cannot resolve deployment root")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Review{}, errors.New("cannot resolve deployment root")
	}
	id, err := newID()
	if err != nil {
		return Review{}, err
	}
	_, canApply := adapter.(ExecutionAdapter)
	now := time.Now().UTC()
	public := Review{ID: id, Kind: spec.Kind, Plan: spec.Plan, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), CanApply: canApply}
	dir := filepath.Join(e.Root, ".stack", "go-reviews")
	if err := privateDirectory(dir); err != nil {
		return Review{}, err
	}
	if err := ctx.Err(); err != nil {
		return Review{}, err
	}
	if err := WriteJSON(filepath.Join(dir, id+".json"), reviewRecord{Version: 1, Root: root, Review: public, Binding: spec.Binding}); err != nil {
		return Review{}, err
	}
	return public, nil
}

func (e Engine) readReview(id string) (reviewRecord, error) {
	var record reviewRecord
	if !operationID.MatchString(id) {
		return record, errors.New("invalid review identifier")
	}
	data, err := os.ReadFile(filepath.Join(e.Root, ".stack", "go-reviews", id+".json"))
	if err != nil || json.Unmarshal(data, &record) != nil || record.Version != 1 || record.Review.ID != id || (record.UsedBy != "" && !operationID.MatchString(record.UsedBy)) {
		return record, errors.New("review is missing or invalid")
	}
	root, err := filepath.EvalSymlinks(e.Root)
	if err != nil {
		return record, errors.New("cannot resolve reviewed deployment")
	}
	root, err = filepath.Abs(root)
	if err != nil || root != record.Root {
		return record, errors.New("review belongs to another deployment")
	}
	spec, err := validateSpec(ReviewSpec{Kind: record.Review.Kind, Plan: record.Review.Plan, Binding: record.Binding})
	if err != nil || !record.Review.ExpiresAt.After(record.Review.CreatedAt) {
		return record, errors.New("invalid saved review")
	}
	record.Binding = spec.Binding
	return record, nil
}

// ApplyReview requires an exact, explicitly confirmed review ID. A repeated
// request returns its existing operation status and never repeats the action.
func (e Engine) ApplyReview(ctx context.Context, id string, confirmed bool, adapter ExecutionAdapter) (OperationStatus, error) {
	if !confirmed {
		return OperationStatus{}, errors.New("explicit review confirmation is required")
	}
	unlock, err := Lock(e.Root)
	if err != nil {
		return OperationStatus{}, err
	}
	defer unlock()
	record, err := e.readReview(id)
	if err != nil {
		return OperationStatus{}, err
	}
	if record.UsedBy != "" {
		status, err := e.Status()
		if err == nil && status.ID == record.UsedBy && status.ReviewID == id {
			return status, nil
		}
		return OperationStatus{}, ErrConsumedReview
	}
	if err := e.CheckIdle(); err != nil {
		return OperationStatus{}, err
	}
	if !record.Review.CanApply {
		return OperationStatus{}, ErrExecutionUnavailable
	}
	if !time.Now().UTC().Before(record.Review.ExpiresAt) {
		return OperationStatus{}, ErrExpiredReview
	}
	if err := ctx.Err(); err != nil {
		return OperationStatus{}, err
	}
	current, err := adapter.ReviewState(ctx, e.Root)
	if err != nil {
		return OperationStatus{}, errors.New("cannot revalidate reviewed inputs and runtime")
	}
	current, err = validateSpec(current)
	currentPlan, _ := json.Marshal(current.Plan)
	reviewedPlan, _ := json.Marshal(record.Review.Plan)
	if err != nil || current.Kind != record.Review.Kind || !bytes.Equal(currentPlan, reviewedPlan) || !bytes.Equal(current.Binding, record.Binding) {
		return OperationStatus{}, ErrStaleReview
	}
	w, err := adapter.OpenReviewed(ctx, e.Root, record.Review.Kind, record.Binding)
	if err != nil {
		return OperationStatus{}, errors.New("cannot open reviewed workflow")
	}
	if err := e.validateWorkflow(w); err != nil || w.Kind() != record.Review.Kind {
		return OperationStatus{}, errors.New("workflow does not match the reviewed adapter")
	}
	opID, err := newID()
	if err != nil {
		return OperationStatus{}, err
	}
	// Reserve before registering the operation. If interrupted in between, this
	// review is burned without effects; a new review is safe, an automatic retry is not.
	record.UsedBy = opID
	if err := WriteJSON(filepath.Join(e.Root, ".stack", "go-reviews", id+".json"), record); err != nil {
		return OperationStatus{}, err
	}
	err = e.runLocked(ctx, w, false, opID, id)
	status, statusErr := e.Status()
	if err != nil {
		return status, err
	}
	return status, statusErr
}

// ResumeReviewed binds recovery to an operation ID, not just its workflow kind.
// Expiry limits initial approval; it never prevents recovery of a started action.
func (e Engine) ResumeReviewed(ctx context.Context, id string, adapter ExecutionAdapter) (OperationStatus, error) {
	if !operationID.MatchString(id) {
		return OperationStatus{}, errors.New("invalid operation identifier")
	}
	unlock, err := Lock(e.Root)
	if err != nil {
		return OperationStatus{}, err
	}
	defer unlock()
	j, exists, err := e.loadJournal()
	if err != nil || !exists || j.ID != id || j.ReviewID == "" {
		return OperationStatus{}, errors.New("no matching reviewed operation to resume")
	}
	record, err := e.readReview(j.ReviewID)
	if err != nil || record.UsedBy != id || record.Review.Kind != j.Kind || !record.Review.CanApply {
		return OperationStatus{}, errors.New("operation and review recovery records do not agree")
	}
	w, err := adapter.OpenReviewed(ctx, e.Root, j.Kind, record.Binding)
	if err != nil {
		return OperationStatus{}, errors.New("cannot open original adapter for recovery")
	}
	if err := e.validateWorkflow(w); err != nil || w.Kind() != j.Kind {
		return OperationStatus{}, errors.New("recovery adapter does not match the operation")
	}
	err = e.runLocked(ctx, w, true, "", "")
	status, statusErr := e.Status()
	if err != nil {
		return status, err
	}
	return status, statusErr
}

// CheckIdle refuses legacy or Go operations awaiting recovery. Callers must
// hold the deployment lock while checking and changing managed state.
func (e Engine) CheckIdle() error {
	if err := legacyIdle(filepath.Join(e.Root, ".stack")); err != nil {
		return err
	}
	j, exists, err := e.loadJournal()
	if err != nil {
		return err
	}
	if exists && j.Status != "complete" && j.Status != "cancelled" {
		return errors.New("a Go operation needs recovery before changing managed state")
	}
	return nil
}

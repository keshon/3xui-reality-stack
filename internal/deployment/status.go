package deployment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// OperationStatus contains public identifiers and progress, never recovery
// payloads or adapter errors. Atomic journal replacement permits unlocked reads.
type OperationStatus struct {
	ID          string `json:"id,omitempty"`
	ReviewID    string `json:"review_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Status      string `json:"status"`
	Completed   int    `json:"completed"`
	Total       int    `json:"total"`
	CurrentStep string `json:"current_step,omitempty"`
}

func (e Engine) loadJournal() (journal, bool, error) {
	var j journal
	data, err := os.ReadFile(filepath.Join(e.Root, ".stack", "go-operation.json"))
	if os.IsNotExist(err) {
		return j, false, nil
	}
	if err != nil || json.Unmarshal(data, &j) != nil || !validJournal(j) {
		return j, false, errors.New("invalid Go operation; preserve it for recovery")
	}
	return j, true, nil
}

func (e Engine) Status() (OperationStatus, error) {
	j, exists, err := e.loadJournal()
	if err != nil {
		return OperationStatus{}, err
	}
	if !exists {
		return OperationStatus{Status: "idle"}, nil
	}
	status := OperationStatus{ID: j.ID, ReviewID: j.ReviewID, Kind: j.Kind, Status: j.Status, Completed: j.Next, Total: len(j.Steps)}
	if j.Status != "cancelled" && j.Next < len(j.Steps) {
		status.CurrentStep = j.Steps[j.Next]
	}
	return status, nil
}

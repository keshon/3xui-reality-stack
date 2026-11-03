// Package xui integrates with the existing managed-state schema and pinned panel
// API. It provides read-only inspection and an experimental password workflow.
package xui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

const stackID = "3x-ui"

type config struct {
	Schema int                 `json:"schema_version"`
	Env    map[string]string   `json:"env"`
	Users  []string            `json:"users"`
	Nodes  []map[string]string `json:"nodes"`
}

// Applied records permit extra metadata, but their nested configuration must
// obey the same schema as the desired file. JSON null is not a literal string.
func (c *config) UnmarshalJSON(data []byte) error {
	type plain config
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return errors.New("unsupported configuration shape")
	}
	var raw struct {
		Env map[string]json.RawMessage `json:"env"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return errors.New("invalid configuration")
	}
	for _, value := range raw.Env {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("environment entries must be strings")
		}
	}
	*c = config(decoded)
	return nil
}

type baseline struct {
	Version int                `json:"version"`
	Config  config             `json:"config"`
	Hashes  map[string]*string `json:"output_hashes"`
}

type Inspection struct {
	Report  deployment.Report
	desired config
	applied config
}

// readJSON never propagates parser errors, which can quote credential values.
func readJSON(path string, target any, strict bool) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot read managed state")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if strict {
		d.DisallowUnknownFields()
	}
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return false, errors.New("invalid managed state JSON")
	}
	return true, nil
}

func valid(c config) bool {
	return c.Schema == 1 && c.Env != nil && c.Users != nil && c.Nodes != nil
}

func Inspect(root string) (Inspection, error) {
	return inspect(root, false)
}

func inspect(root string, ownPreparation bool) (Inspection, error) {
	i := Inspection{Report: deployment.Report{Stack: stackID, Blockers: []string{}, Observation: "local metadata only; live services not checked"}}
	state := filepath.Join(root, ".stack")
	var err error
	i.Report.Managed, err = readJSON(filepath.Join(state, "config.json"), &i.desired, true)
	if err != nil {
		return i, err
	}
	if !i.Report.Managed {
		i.Report.Blockers = append(i.Report.Blockers, "no managed configuration; installation and import are not implemented")
	} else if !valid(i.desired) {
		return i, errors.New("unsupported managed configuration schema")
	}
	var previous baseline
	i.Report.Baseline, err = readJSON(filepath.Join(state, "applied.json"), &previous, false)
	if err != nil {
		return i, err
	}
	if !i.Report.Baseline {
		if _, err := os.Lstat(filepath.Join(state, "go-initialization.json")); err == nil {
			i.Report.Blockers = append(i.Report.Blockers, "Go initialization draft has no applied baseline; fresh-install execution is not implemented")
		} else {
			i.Report.Blockers = append(i.Report.Blockers, "no applied baseline; adoption is not implemented")
		}
	} else {
		if previous.Version != 1 || !valid(previous.Config) {
			return i, errors.New("unsupported applied baseline schema")
		}
		i.applied = previous.Config
		if err := checkOutputs(root, previous.Hashes); err != nil {
			i.Report.Blockers = append(i.Report.Blockers, err.Error())
		}
	}
	for _, journal := range []struct {
		name     string
		terminal []string
	}{
		{"operation.json", []string{"complete"}}, {"adoption.json", []string{"complete"}}, {"upgrade.json", []string{"complete", "rolled_back"}},
	} {
		var entry struct {
			Status string `json:"status"`
		}
		exists, err := readJSON(filepath.Join(state, journal.name), &entry, false)
		if err != nil {
			return i, err
		}
		// Unknown statuses fail closed. Recovery stays with the current controller.
		if exists && !slices.Contains(journal.terminal, entry.Status) {
			i.Report.Blockers = append(i.Report.Blockers, "unfinished or unsupported "+journal.name+"; use the existing controller for recovery")
		}
	}
	if !ownPreparation {
		var marker struct {
			Version int    `json:"version"`
			Status  string `json:"status"`
		}
		exists, err := readJSON(filepath.Join(state, "go-operation.json"), &marker, false)
		if err != nil {
			return i, err
		}
		if exists && (marker.Version != 1 || (marker.Status != "complete" && marker.Status != "cancelled")) {
			i.Report.Blockers = append(i.Report.Blockers, "Go operation needs recovery with its original adapter")
		}
	}
	return i, nil
}

func checkOutputs(root string, hashes map[string]*string) error {
	names := []string{".env", "users.txt", "nodes.txt"}
	if len(hashes) != len(names) {
		return errors.New("unsupported generated-file baseline")
	}
	for _, name := range names {
		expected, ok := hashes[name]
		if !ok {
			return errors.New("unsupported generated-file baseline")
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) && expected == nil {
			continue
		}
		if err != nil || expected == nil {
			return errors.New("generated files differ from applied baseline or cannot be read")
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != *expected {
			return errors.New("generated files differ from applied baseline or cannot be read")
		}
	}
	return nil
}

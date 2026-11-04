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
	"runtime"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// readDraft is shared by initialization retry, preflight and review. Callers
// performing effects hold the deployment lock; read-only callers get a snapshot.
func readDraft(root string) (initialization, map[string][]byte, error) {
	return readDraftState(root, false)
}

// ownOperation is reserved for a workflow already holding the engine lock.
func readDraftState(root string, ownOperation bool) (initialization, map[string][]byte, error) {
	return readInstallDraft(root, ownOperation, false)
}

func readInstallDraft(root string, ownOperation, started bool) (initialization, map[string][]byte, error) {
	return readInstallDraftPhase(root, ownOperation, started, false)
}

func readInstallDraftPhase(root string, ownOperation, started, panelStarted bool) (initialization, map[string][]byte, error) {
	return readInstallDraftCommit(root, ownOperation, started, panelStarted, nil)
}

// Only commit replay may admit its exact, already frozen applied record.
// Initialization, preflight and review continue to reject every applied root.
func readInstallDraftCommit(root string, ownOperation, started, panelStarted bool, applied []byte) (initialization, map[string][]byte, error) {
	fail := func() (initialization, map[string][]byte, error) {
		return initialization{}, nil, errors.New("initialization draft is absent, changed or already in use")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail()
	}
	info, err = os.Lstat(filepath.Join(root, ".stack"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail()
	}
	if _, err := os.Lstat(filepath.Join(root, ".stack/applied.json")); !os.IsNotExist(err) {
		if len(applied) == 0 {
			return fail()
		}
		actual, err := privatePromotionFile(filepath.Join(root, ".stack"), "applied.json", 4<<20)
		if err != nil || !bytes.Equal(actual, applied) {
			return fail()
		}
	}
	if err := validateDraftDirectories(root); err != nil {
		return fail()
	}
	if !ownOperation {
		if err := (deployment.Engine{Root: root}).CheckIdle(); err != nil {
			return fail()
		}
	}
	for _, name := range []string{"xui/db", "traefik/data/logs", "shared/certs/certs", "shared/certs/private"} {
		if panelStarted && name == "xui/db" {
			if err := validateBootstrapDatabase(filepath.Join(root, name)); err != nil {
				return fail()
			}
			continue
		}
		if started && name != "xui/db" {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, name))
		if err != nil || len(entries) != 0 {
			return fail()
		}
	}
	read := func(name string, limit int64) ([]byte, error) {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit || runtime.GOOS != "windows" && info.Mode().Perm() != artifactMode(name) {
			return nil, errors.New("invalid draft file")
		}
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			return nil, errors.New("cannot open draft file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil || int64(len(data)) > limit {
			return nil, errors.New("cannot read bounded draft file")
		}
		return data, nil
	}
	marker, err := read(".stack/go-initialization.json", 32<<10)
	var state initialization
	d := json.NewDecoder(bytes.NewReader(marker))
	d.DisallowUnknownFields()
	if err != nil || !json.Valid(marker) || d.Decode(&state) != nil || state.Version != 1 || len(state.Files) != len(initializationFiles) {
		return fail()
	}
	if hash, err := hex.DecodeString(state.InputHash); err != nil || len(hash) != sha256.Size {
		return fail()
	}
	inputs := map[string][]byte{".stack/go-initialization.json": marker}
	for _, name := range initializationFiles {
		if started && name == acmeStateFile {
			info, err := os.Lstat(filepath.Join(root, name))
			if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				return fail()
			}
			inputs[name] = []byte{}
			continue
		}
		data, err := read(name, 4<<20)
		sum := sha256.Sum256(data)
		if err != nil || state.Files[name] != hex.EncodeToString(sum[:]) {
			return fail()
		}
		inputs[name] = data
	}
	return state, inputs, nil
}

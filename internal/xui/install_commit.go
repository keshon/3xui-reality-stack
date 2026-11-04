package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type installCommit struct {
	Version   int               `json:"version"`
	Config    config            `json:"config"`
	Hashes    map[string]string `json:"output_hashes"`
	AppliedAt string            `json:"applied_at"`
}

func (w *installRuntime) commitInstallation(ctx context.Context, dir string, snapshot installRuntimeSnapshot) error {
	var c config
	if json.Unmarshal(snapshot.Binding.Inputs[".stack/config.json"], &c) != nil {
		return errors.New("invalid installation commit configuration")
	}
	hashes := map[string]string{}
	for _, name := range []string{".env", "users.txt", "nodes.txt"} {
		hashes[name] = promotionDigest(snapshot.Binding.Inputs[name])
	}
	// Freeze once. A lost engine checkpoint must not change applied_at or admit
	// an unrelated baseline. The engine already owns the operation directory.
	file := filepath.Join(dir, "install-commit.json")
	if _, err := os.Lstat(file); os.IsNotExist(err) {
		if _, err := os.Lstat(filepath.Join(w.root, ".stack/applied.json")); !os.IsNotExist(err) {
			return errors.New("unexpected applied installation state")
		}
		if err := deployment.WriteJSON(file, installCommit{1, c, hashes, time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			return err
		}
	} else if err != nil {
		return errors.New("cannot inspect frozen installation commit")
	}
	target, err := privatePromotionFile(dir, "install-commit.json", 4<<20)
	var saved installCommit
	if err != nil || json.Unmarshal(target, &saved) != nil || saved.Version != 1 || !reflect.DeepEqual(saved.Config, c) || !reflect.DeepEqual(saved.Hashes, hashes) {
		return errors.New("installation commit does not match the reviewed inputs")
	}
	if stamp, err := time.Parse(time.RFC3339Nano, saved.AppliedAt); err != nil || stamp.IsZero() {
		return errors.New("invalid installation commit timestamp")
	}
	w.commitBaseline = target
	defer func() { w.commitBaseline = nil }()
	// Re-run the existing gates on commit/replay rather than introduce another
	// durable verification receipt or a separate freshness policy.
	if err := w.verifyInstallation(ctx, dir, snapshot); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(w.root, ".stack/applied.json")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		actual, readErr := privatePromotionFile(filepath.Join(w.root, ".stack"), "applied.json", 4<<20)
		if readErr != nil || !bytes.Equal(actual, target) {
			return errors.New("applied installation state changed")
		}
		return nil
	}
	return deployment.WriteFile(path, target)
}

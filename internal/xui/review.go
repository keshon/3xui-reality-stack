package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type passwordBinding struct {
	Inputs  map[string][]byte  `json:"inputs"`
	Policy  []byte             `json:"policy"`
	Runtime runtimeObservation `json:"runtime"`
}

// ReviewState records a private point-in-time observation. Stack intentionally
// does not implement ExecutionAdapter. PasswordStack is a separate opt-in.
func (s Stack) ReviewState(ctx context.Context, root string) (deployment.ReviewSpec, error) {
	if _, err := os.Lstat(filepath.Join(root, ".stack/go-initialization.json")); err == nil {
		if _, err := os.Lstat(filepath.Join(root, ".stack/applied.json")); os.IsNotExist(err) {
			return s.reviewInstall(ctx, root)
		}
	}
	before, err := readPasswordInputs(root)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	policy, err := os.ReadFile(filepath.Join(root, "compatibility.json"))
	if err != nil {
		return deployment.ReviewSpec{}, errors.New("compatibility policy unavailable")
	}
	i, err := Inspect(root)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	if i.Plan().Status != "proposed" {
		return deployment.ReviewSpec{}, errors.New("no applicable isolated password plan")
	}
	runtime, observation, err := s.observeRuntime(ctx, root)
	if err != nil || runtime.Status != "compatible" {
		return deployment.ReviewSpec{}, errors.New("runtime is not compatible for password review")
	}
	after, err := readPasswordInputs(root)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	for _, name := range passwordInputs {
		if !bytes.Equal(before[name], after[name]) {
			return deployment.ReviewSpec{}, errors.New("configuration changed during review")
		}
	}
	currentPolicy, err := os.ReadFile(filepath.Join(root, "compatibility.json"))
	if err != nil || !bytes.Equal(policy, currentPolicy) {
		return deployment.ReviewSpec{}, errors.New("compatibility policy changed during review")
	}
	binding, err := json.Marshal(passwordBinding{before, policy, observation})
	return deployment.ReviewSpec{Kind: "xui-password-v1", Plan: i.Plan(), Binding: binding}, err
}

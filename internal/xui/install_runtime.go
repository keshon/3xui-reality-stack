package xui

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

type helperInstaller interface {
	Ensure(context.Context, string, []byte, string) error
}

// installRuntime executes the standalone installation bound by InstallStack.
type installRuntime struct {
	root             string
	stack            Stack
	binding          *installBinding
	helper           helperInstaller
	webReady         func(context.Context, string, map[string][]byte) error
	panelEndpoint    func(webContainer, string) (*Panel, error)
	publishedPanel   func(config) (*Panel, error)
	subscriptionRead func(context.Context, config, string) ([]byte, error)
	jsonRead         func(context.Context, config, string) ([]byte, error)
	transferProbe    func(context.Context, string, string, []byte) error
	commitBaseline   []byte
}

type installRuntimeSnapshot struct {
	Version int            `json:"version"`
	Root    string         `json:"root"`
	Owner   string         `json:"owner"`
	Binding installBinding `json:"binding"`
}

func (w *installRuntime) Kind() string           { return "xui-install-v1" }
func (w *installRuntime) DeploymentRoot() string { return w.root }
func (w *installRuntime) docker() dockerCommand {
	if w.stack.docker != nil {
		return w.stack.docker
	}
	return localDocker
}

func (w *installRuntime) Prepare(ctx context.Context, dir string) error {
	if w.binding == nil {
		return errors.New("installation requires the original review binding")
	}
	if _, err := os.Lstat(filepath.Join(dir, "installation.json")); err == nil {
		_, err := w.open(dir)
		return err
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect install recovery state")
	}
	owner := filepath.Base(dir)
	id, err := hex.DecodeString(owner)
	if err != nil || len(id) != 16 {
		return errors.New("invalid installation operation identity")
	}
	snapshot := installRuntimeSnapshot{Version: 1, Root: w.root, Owner: owner, Binding: *w.binding}
	if err := w.check(ctx, snapshot, "", false); err != nil {
		return err
	}
	if err := deployment.WriteFile(filepath.Join(dir, "compose.env"), []byte{}); err != nil {
		return err
	}
	return deployment.WriteJSON(filepath.Join(dir, "installation.json"), snapshot)
}

func (w *installRuntime) open(dir string) (installRuntimeSnapshot, error) {
	var saved installRuntimeSnapshot
	exists, err := readJSON(filepath.Join(dir, "installation.json"), &saved, true)
	id, idErr := hex.DecodeString(saved.Owner)
	if err != nil || !exists || saved.Version != 1 || saved.Root != w.root || saved.Owner != filepath.Base(dir) || idErr != nil || len(id) != 16 || !saved.Binding.Helper.ServiceState.Absent() || len(saved.Binding.Inputs) != len(initializationFiles)+1 || w.binding != nil && !reflect.DeepEqual(*w.binding, saved.Binding) {
		return saved, errors.New("installation recovery does not match the original review")
	}
	w.binding = &saved.Binding
	info, err := os.Lstat(filepath.Join(dir, "compose.env"))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return saved, errors.New("frozen Compose environment changed")
	}
	return saved, nil
}

func networkReceipt(dir string) (string, error) {
	var receipt struct {
		ID string `json:"id"`
	}
	exists, err := readJSON(filepath.Join(dir, "network.json"), &receipt, true)
	if err != nil || exists && !dockerID(receipt.ID) {
		return "", errors.New("invalid installation network receipt")
	}
	return receipt.ID, nil
}

func (w *installRuntime) check(ctx context.Context, snapshot installRuntimeSnapshot, networkID string, allowService bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, inputs, err := readDraftState(w.root, true)
	if err != nil || !reflect.DeepEqual(inputs, snapshot.Binding.Inputs) {
		return errors.New("frozen installation files changed")
	}
	probe := w.stack.preflightHost
	if probe == nil {
		probe = host.Probe
	}
	observed, err := probe(ctx, []deployment.Port{{Protocol: "tcp", Number: 80}, {Protocol: "tcp", Number: 443}, {Protocol: "udp", Number: 443}, {Protocol: "tcp", Number: 2053}})
	if err != nil || observed.Facts.OS != snapshot.Binding.Host.OS || observed.Facts.Architecture != snapshot.Binding.Host.Architecture || observed.Facts.Distribution != snapshot.Binding.Host.Distribution || observed.Facts.Containers != 0 || observed.Facts.Volumes != 0 || len(observed.Checks) == 0 {
		return errors.New("installation host prerequisites or existing applications changed")
	}
	network, err := observeInstallNetwork(ctx, w.docker())
	if err != nil {
		return err
	}
	count := 0
	if network != nil {
		if !ownedNetwork(network, w.root, snapshot.Owner, networkID) {
			return errors.New("installation network ownership changed")
		}
		count = 1
	} else if networkID != "" {
		return errors.New("owned network disappeared")
	}
	if observed.Facts.CustomNetworks != count {
		return errors.New("unexpected host networks appeared")
	}
	for _, check := range observed.Checks {
		if !check.OK && !(check.ID == "existing-networks" && count == 1) {
			return errors.New("installation host has an unresolved resource conflict")
		}
	}
	observe := w.stack.installHost
	if observe == nil {
		observe = observeInstallHost
	}
	var c config
	if json.Unmarshal(inputs[".stack/config.json"], &c) != nil {
		return errors.New("invalid frozen installation configuration")
	}
	current, err := observe(ctx, w.root, c.Env["CERT_BASENAME"])
	if err != nil || current.Controller != snapshot.Binding.Helper.Controller || !reflect.DeepEqual(current.Service, snapshot.Binding.Helper.Service) || !allowService && !current.ServiceState.Absent() {
		return errors.New("reviewed controller or helper service ownership changed")
	}
	return ctx.Err()
}

func (w *installRuntime) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	snapshot, err := w.open(dir)
	if err != nil {
		return nil, err
	}
	check := func(ctx context.Context, allowService bool) error {
		id, err := networkReceipt(dir)
		if err != nil {
			return err
		}
		return w.check(ctx, snapshot, id, allowService)
	}
	steps := []deployment.Step{
		{ID: "validate-compose", Run: func(ctx context.Context) error {
			if err := check(ctx, false); err != nil {
				return err
			}
			for _, project := range []string{"traefik", "decoy", "xui"} {
				if _, err := w.docker()(ctx, "compose", "--env-file", filepath.Join(dir, "compose.env"), "--project-directory", filepath.Join(w.root, project), "-f", filepath.Join(w.root, project, "compose.json"), "config", "--quiet"); err != nil {
					return errors.New("generated Compose validation failed")
				}
			}
			return check(ctx, false)
		}},
		{ID: "network", Run: func(ctx context.Context) error {
			if err := check(ctx, false); err != nil {
				return err
			}
			expected, err := networkReceipt(dir)
			if err != nil {
				return err
			}
			id, err := ensureInstallNetwork(ctx, w.docker(), w.root, snapshot.Owner, expected)
			if err != nil {
				return err
			}
			if err := deployment.WriteJSON(filepath.Join(dir, "network.json"), struct {
				ID string `json:"id"`
			}{id}); err != nil {
				return err
			}
			return check(ctx, false)
		}},
		{ID: "certificate-service", Run: func(ctx context.Context) error {
			if err := check(ctx, true); err != nil {
				return err
			}
			helper := w.helper
			if helper == nil {
				helper = host.NewServiceInstaller()
			}
			if err := helper.Ensure(ctx, snapshot.Binding.Helper.Service.Name, snapshot.Binding.Helper.Service.Data, snapshot.Owner); err != nil {
				return err
			}
			return check(ctx, true)
		}},
		{ID: "services", Run: func(ctx context.Context) error { return w.startWeb(ctx, dir, snapshot) }},
		{ID: "panel", Run: func(ctx context.Context) error {
			if err := w.startPanel(ctx, dir, snapshot); err != nil {
				return err
			}
			return w.provisionPanel(ctx, dir, snapshot)
		}},
	}
	steps = append(steps, deployment.Step{ID: "verify", Run: func(ctx context.Context) error {
		return w.verifyInstallation(ctx, dir, snapshot)
	}}, deployment.Step{ID: "commit", Run: func(ctx context.Context) error { return w.commitInstallation(ctx, dir, snapshot) }})
	return steps, ctx.Err()
}

// Bind only the reviewed bytes, never a newly observed replacement snapshot.
func decodeInstallBinding(raw json.RawMessage) (installBinding, error) {
	var binding installBinding
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !json.Valid(raw) || d.Decode(&binding) != nil || len(binding.Inputs) != len(initializationFiles)+1 || !binding.Helper.ServiceState.Absent() {
		return binding, errors.New("invalid frozen installation review")
	}
	return binding, nil
}

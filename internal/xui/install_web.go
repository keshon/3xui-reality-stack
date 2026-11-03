package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

const webInventoryTemplate = `{"id":{{json .ID}},"name":{{json .Names}}}`
const webInspectTemplate = `{"id":{{json .Id}},"image_id":{{json .Image}},"name":{{json .Name}},"running":{{json .State.Running}},"started_at":{{json .State.StartedAt}},"mounts":{{json .Mounts}},"ports":{{json .NetworkSettings.Ports}},"networks":{{json .NetworkSettings.Networks}},"installation":{{json (index .Config.Labels "io.stackctl.installation")}},"operation":{{json (index .Config.Labels "io.stackctl.operation")}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}}}`

type webContainer struct {
	container
	Name         string                                `json:"name"`
	Installation string                                `json:"installation"`
	Operation    string                                `json:"operation"`
	Project      string                                `json:"project"`
	Service      string                                `json:"service"`
	Networks     map[string]struct{ NetworkID string } `json:"networks"`
}

type webIntent struct {
	Version    int               `json:"version"`
	Owner      string            `json:"owner"`
	Images     map[string]string `json:"images"`
	References map[string]string `json:"references"`
}
type webIdentity struct {
	ID        string `json:"id"`
	ImageID   string `json:"image_id"`
	StartedAt string `json:"started_at"`
}

func validImageID(id string) bool {
	return strings.HasPrefix(id, "sha256:") && dockerID(strings.TrimPrefix(id, "sha256:"))
}
func (w *installRuntime) webDocker() dockerCommand {
	if w.stack.docker != nil {
		return w.stack.docker
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return localDockerTimeout(ctx, 2*time.Minute, args...)
	}
}

func webOverride(snapshot installRuntimeSnapshot, project, image string) map[string]any {
	return map[string]any{"services": map[string]any{project: map[string]any{"image": image, "labels": map[string]string{"io.stackctl.installation": installationOwner(snapshot.Root), "io.stackctl.operation": snapshot.Owner}}}}
}

func imageRepository(reference string) string {
	repository, _, _ := strings.Cut(reference, "@")
	if colon := strings.LastIndex(repository, ":"); colon > strings.LastIndex(repository, "/") {
		repository = repository[:colon]
	}
	repository = strings.TrimPrefix(repository, "docker.io/")
	return strings.TrimPrefix(repository, "library/")
}

func resolvedWebImage(data []byte, requested string) (string, string, error) {
	var image struct {
		ID      string   `json:"id"`
		Digests []string `json:"digests"`
	}
	if json.Unmarshal(data, &image) != nil || !validImageID(image.ID) {
		return "", "", errors.New("invalid web image metadata")
	}
	for _, reference := range image.Digests {
		_, digest, ok := strings.Cut(reference, "@")
		if ok && validImageID(digest) && imageRepository(reference) == imageRepository(requested) {
			return image.ID, reference, nil
		}
	}
	return "", "", errors.New("web image has no immutable repository digest")
}

func (w *installRuntime) webIntent(ctx context.Context, dir string, snapshot installRuntimeSnapshot) (webIntent, error) {
	var intent webIntent
	exists, err := readJSON(filepath.Join(dir, "web-intent.json"), &intent, true)
	if err != nil {
		return intent, err
	}
	if !exists {
		networkID, err := networkReceipt(dir)
		if err != nil {
			return intent, err
		}
		if err := w.check(ctx, snapshot, networkID, true); err != nil {
			return intent, err
		}
		intent = webIntent{Version: 1, Owner: snapshot.Owner, Images: map[string]string{}, References: map[string]string{}}
		for _, project := range []string{"decoy", "traefik"} {
			var doc composeDocument
			if json.Unmarshal(snapshot.Binding.Inputs[project+"/compose.json"], &doc) != nil {
				return intent, errors.New("invalid reviewed web Compose document")
			}
			image := doc.Services[project].Image
			const format = `{"id":{{json .Id}},"digests":{{json .RepoDigests}}}`
			data, inspectErr := w.webDocker()(ctx, "image", "inspect", image, "--format", format)
			id, reference, resolveErr := resolvedWebImage(data, image)
			if inspectErr != nil || resolveErr != nil {
				if _, err := w.webDocker()(ctx, "pull", image); err != nil {
					return intent, errors.New("cannot obtain pinned web image")
				}
				data, inspectErr = w.webDocker()(ctx, "image", "inspect", image, "--format", format)
				id, reference, resolveErr = resolvedWebImage(data, image)
			}
			if inspectErr != nil || resolveErr != nil {
				return intent, errors.New("cannot resolve immutable web image")
			}
			intent.Images[project] = id
			intent.References[project] = reference
			if err := deployment.WriteJSON(filepath.Join(dir, "web-"+project+".json"), webOverride(snapshot, project, reference)); err != nil {
				return intent, err
			}
		}
		if err := deployment.WriteJSON(filepath.Join(dir, "web-intent.json"), intent); err != nil {
			return intent, err
		}
	}
	if intent.Version != 1 || intent.Owner != snapshot.Owner || len(intent.Images) != 2 || len(intent.References) != 2 {
		return intent, errors.New("invalid web startup intent")
	}
	for _, project := range []string{"decoy", "traefik"} {
		if !validImageID(intent.Images[project]) {
			return intent, errors.New("invalid frozen web image")
		}
		var doc composeDocument
		json.Unmarshal(snapshot.Binding.Inputs[project+"/compose.json"], &doc)
		_, digest, ok := strings.Cut(intent.References[project], "@")
		if !ok || !validImageID(digest) || imageRepository(intent.References[project]) != imageRepository(doc.Services[project].Image) {
			return intent, errors.New("frozen web repository digest changed")
		}
		var current map[string]any
		ok, err := readJSON(filepath.Join(dir, "web-"+project+".json"), &current, false)
		expected, _ := json.Marshal(webOverride(snapshot, project, intent.References[project]))
		actual, _ := json.Marshal(current)
		if err != nil || !ok || !bytes.Equal(expected, actual) {
			return intent, errors.New("frozen web startup override changed")
		}
	}
	return intent, nil
}

func (w *installRuntime) observeWeb(ctx context.Context, snapshot installRuntimeSnapshot, intent webIntent, dir string) (map[string]webContainer, error) {
	return w.observeInstallServices(ctx, snapshot, intent, dir, nil)
}

func (w *installRuntime) observeInstallServices(ctx context.Context, snapshot installRuntimeSnapshot, intent webIntent, dir string, panel *panelIntent) (map[string]webContainer, error) {
	_, inputs, err := readInstallDraftPhase(w.root, true, true, panel != nil)
	if err != nil || !reflect.DeepEqual(inputs, snapshot.Binding.Inputs) {
		return nil, errors.New("frozen installation inputs changed during web startup")
	}
	networkID, err := networkReceipt(dir)
	if err != nil || networkID == "" {
		return nil, errors.New("owned web network receipt unavailable")
	}
	network, err := observeInstallNetwork(ctx, w.docker())
	if err != nil || !ownedNetworkConfiguration(network, w.root, snapshot.Owner, networkID) {
		return nil, errors.New("owned web network changed")
	}
	data, err := w.webDocker()(ctx, "ps", "--all", "--no-trunc", "--format", webInventoryTemplate)
	if err != nil {
		return nil, errors.New("cannot inspect web startup inventory")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	found := map[string]webContainer{}
	ids := map[string]bool{}
	for {
		var row struct{ ID, Name string }
		err := d.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil || !dockerID(row.ID) || ids[row.ID] {
			return nil, errors.New("unexpected containers appeared during web startup")
		}
		if _, exists := found[row.Name]; exists {
			return nil, errors.New("ambiguous web container name")
		}
		if panel != nil && row.Name == panelContainerName(snapshot.Owner) {
			current, err := w.inspectBootstrapPanel(ctx, snapshot, *panel, row.ID)
			if err != nil {
				return nil, err
			}
			found[row.Name], ids[row.ID] = current, true
			continue
		}
		if row.Name != "traefik" && row.Name != "decoy" {
			return nil, errors.New("unexpected containers appeared during web startup")
		}
		data, err := w.webDocker()(ctx, "inspect", row.ID, "--format", webInspectTemplate)
		var current webContainer
		if err != nil || json.Unmarshal(data, &current) != nil || current.ID != row.ID || !validWebContainer(current, row.Name, w.root, snapshot.Owner, networkID, intent.Images[row.Name], snapshot.Binding.Inputs[row.Name+"/compose.json"]) {
			return nil, errors.New("web container identity or layout does not match the operation")
		}
		var saved webIdentity
		exists, err := readJSON(filepath.Join(dir, "web-"+row.Name+"-identity.json"), &saved, true)
		if err != nil || exists && (saved.ID != current.ID || saved.ImageID != current.ImageID || saved.StartedAt != current.StartedAt || !current.Running) {
			return nil, errors.New("recorded web container was replaced, restarted or stopped")
		}
		found[row.Name] = current
		ids[current.ID] = true
	}
	for _, project := range []string{"decoy", "traefik"} {
		if _, ok := found[project]; !ok {
			if _, err := os.Lstat(filepath.Join(dir, "web-"+project+"-identity.json")); !os.IsNotExist(err) {
				return nil, errors.New("recorded web container disappeared")
			}
		}
	}
	if network.Containers != len(network.Endpoints) {
		return nil, errors.New("incomplete web network endpoint inventory")
	}
	for id := range network.Endpoints {
		if !ids[id] {
			return nil, errors.New("foreign endpoint attached to the owned web network")
		}
	}
	probe := w.stack.preflightHost
	if probe == nil {
		probe = host.Probe
	}
	observed, err := probe(ctx, []deployment.Port{{Protocol: "tcp", Number: 80}, {Protocol: "tcp", Number: 443}, {Protocol: "udp", Number: 443}, {Protocol: "tcp", Number: 2053}})
	if err != nil || observed.Facts.OS != snapshot.Binding.Host.OS || observed.Facts.Architecture != snapshot.Binding.Host.Architecture || observed.Facts.Distribution != snapshot.Binding.Host.Distribution || observed.Facts.Containers != len(found) || observed.Facts.CustomNetworks != 1 || observed.Facts.Volumes != 0 || len(observed.Checks) == 0 {
		return nil, errors.New("web startup host prerequisites or resources changed")
	}
	for _, check := range observed.Checks {
		allowed := check.ID == "existing-networks" || check.ID == "existing-containers" && len(found) > 0 || (check.ID == "port-tcp-80" || check.ID == "port-tcp-2053") && found["traefik"].Running
		if !check.OK && !allowed {
			return nil, errors.New("web startup host resource conflict")
		}
	}
	var c config
	json.Unmarshal(snapshot.Binding.Inputs[".stack/config.json"], &c)
	observe := w.stack.installHost
	if observe == nil {
		observe = observeInstallHost
	}
	helper, err := observe(ctx, w.root, c.Env["CERT_BASENAME"])
	if err != nil || helper.Controller != snapshot.Binding.Helper.Controller || !reflect.DeepEqual(helper.Service, snapshot.Binding.Helper.Service) || helper.ServiceState.LoadState != "loaded" || helper.ServiceState.ActiveState != "active" || helper.ServiceState.FileState != "enabled" || helper.ServiceState.DropIns != "" {
		return nil, errors.New("reviewed certificate helper or controller is not ready")
	}
	return found, ctx.Err()
}

func validWebContainer(current webContainer, project, root, owner, networkID, imageID string, raw []byte) bool {
	if current.Name != "/"+project || current.Installation != installationOwner(root) || current.Operation != owner || current.Project != project || current.Service != project || current.ImageID != imageID || len(current.Networks) != 1 || current.Networks["proxy"].NetworkID != networkID {
		return false
	}
	var doc composeDocument
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	expected := doc.Services[project]
	if len(current.Mounts) != len(expected.Volumes) {
		return false
	}
	mounts := map[string]bool{}
	for _, volume := range expected.Volumes {
		parts := strings.Split(volume, ":")
		source := parts[0]
		if !path.IsAbs(source) {
			source = filepath.Clean(filepath.Join(root, project, source))
		}
		readWrite := len(parts) < 3 || parts[2] != "ro"
		matched := false
		for _, mount := range current.Mounts {
			if mount.Type == "bind" && mount.Source == source && mount.Destination == parts[1] && mount.RW == readWrite && !mounts[mount.Destination] {
				mounts[mount.Destination] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	publicHTTP, management := false, false
	for port, bindings := range current.Ports {
		for _, binding := range bindings {
			if project != "traefik" {
				return false
			}
			switch {
			case port == "80/tcp" && binding.HostPort == "80" && (binding.HostIP == "0.0.0.0" || binding.HostIP == "::"):
				publicHTTP = true
			case port == "8443/tcp" && binding.HostPort == "2053" && binding.HostIP == "127.0.0.1":
				management = true
			default:
				return false
			}
		}
	}
	return project == "decoy" || publicHTTP && management
}

func (w *installRuntime) startWeb(ctx context.Context, dir string, snapshot installRuntimeSnapshot) error {
	intent, err := w.webIntent(ctx, dir, snapshot)
	if err != nil {
		return err
	}
	helper := w.helper
	if helper == nil {
		helper = host.NewServiceInstaller()
	}
	if err := helper.Ensure(ctx, snapshot.Binding.Helper.Service.Name, snapshot.Binding.Helper.Service.Data, snapshot.Owner); err != nil {
		return err
	}
	for _, project := range []string{"decoy", "traefik"} {
		found, err := w.observeWeb(ctx, snapshot, intent, dir)
		if err != nil {
			return err
		}
		before, exists := found[project]
		if !exists || !before.Running {
			_, _ = w.webDocker()(ctx, "compose", "--env-file", filepath.Join(dir, "compose.env"), "--project-directory", filepath.Join(w.root, project), "-f", filepath.Join(w.root, project, "compose.json"), "-f", filepath.Join(dir, "web-"+project+".json"), "up", "-d", "--no-build", "--no-deps", "--no-recreate", "--pull", "never", project)
			found, err = w.observeWeb(ctx, snapshot, intent, dir)
			if err != nil {
				return err
			}
			current, ok := found[project]
			if !ok || !current.Running || exists && current.ID != before.ID {
				return errors.New("owned web container is not running after startup")
			}
		}
		current := found[project]
		if current.StartedAt == "" {
			return errors.New("web container start identity unavailable")
		}
		if err := deployment.WriteJSON(filepath.Join(dir, "web-"+project+"-identity.json"), webIdentity{current.ID, current.ImageID, current.StartedAt}); err != nil {
			return err
		}
	}
	if err := helper.Ensure(ctx, snapshot.Binding.Helper.Service.Name, snapshot.Binding.Helper.Service.Data, snapshot.Owner); err != nil {
		return err
	}
	ready := w.webReady
	if ready == nil {
		ready = verifyWebCertificates
	}
	if err := ready(ctx, w.root, snapshot.Binding.Inputs); err != nil {
		return err
	}
	_, err = w.observeWeb(ctx, snapshot, intent, dir)
	return err
}

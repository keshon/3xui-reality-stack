package xui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type panelIntent struct {
	Version   int    `json:"version"`
	Owner     string `json:"owner"`
	ImageID   string `json:"image_id"`
	Reference string `json:"reference"`
}

func panelContainerName(owner string) string { return "stackctl-panel-" + owner }

func bootstrapCreateArgs(root string, snapshot installRuntimeSnapshot, intent panelIntent) []string {
	return []string{
		"create", "--pull", "never", "--name", panelContainerName(snapshot.Owner),
		"--label", "io.stackctl.installation=" + installationOwner(root),
		"--label", "io.stackctl.operation=" + snapshot.Owner,
		"--label", "traefik.enable=false", "--network", "bridge",
		"--publish", "127.0.0.1::2053/tcp", "--env", "TZ=Etc/UTC",
		"--mount", "type=bind,src=" + filepath.Join(root, "xui/db") + ",dst=/etc/x-ui",
		"--mount", "type=bind,src=" + filepath.Join(root, "shared/certs") + ",dst=/certs,readonly",
		"--entrypoint", "/app/x-ui", intent.Reference, "run",
	}
}

// SQLite and the pinned panel's metrics store create these sidecars. This
// exception belongs only to an installation with durable panel startup intent.
func validateBootstrapDatabase(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.New("cannot inspect bootstrap database")
	}
	for _, entry := range entries {
		name := entry.Name()
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 || (name != "x-ui.db" && name != "x-ui.db-wal" && name != "x-ui.db-shm" && name != "x-ui.db-journal" && name != "system_metrics.gob" && name != "system_metrics.gob.tmp") {
			return errors.New("unexpected files in bootstrap database directory")
		}
	}
	return nil
}

func (w *installRuntime) panelIntent(ctx context.Context, dir string, snapshot installRuntimeSnapshot, web webIntent) (panelIntent, error) {
	var intent panelIntent
	exists, err := readJSON(filepath.Join(dir, "panel-intent.json"), &intent, true)
	if err != nil {
		return intent, err
	}
	var doc composeDocument
	if json.Unmarshal(snapshot.Binding.Inputs["xui/compose.json"], &doc) != nil {
		return intent, errors.New("invalid reviewed panel configuration")
	}
	image := doc.Services["3x-ui"].Image
	if !exists {
		if _, err := w.observeWeb(ctx, snapshot, web, dir); err != nil {
			return intent, err
		}
		const format = `{"id":{{json .Id}},"digests":{{json .RepoDigests}}}`
		data, inspectErr := w.webDocker()(ctx, "image", "inspect", image, "--format", format)
		id, reference, resolveErr := resolvedWebImage(data, image)
		if inspectErr != nil || resolveErr != nil {
			if _, err := w.webDocker()(ctx, "pull", image); err != nil {
				return intent, errors.New("cannot obtain pinned panel image")
			}
			data, inspectErr = w.webDocker()(ctx, "image", "inspect", image, "--format", format)
			id, reference, resolveErr = resolvedWebImage(data, image)
		}
		if inspectErr != nil || resolveErr != nil {
			return intent, errors.New("cannot resolve immutable panel image")
		}
		intent = panelIntent{1, snapshot.Owner, id, reference}
		if _, err := w.observeWeb(ctx, snapshot, web, dir); err != nil {
			return intent, err
		}
		if err := deployment.WriteJSON(filepath.Join(dir, "panel-intent.json"), intent); err != nil {
			return intent, err
		}
	}
	_, digest, ok := strings.Cut(intent.Reference, "@")
	if intent.Version != 1 || intent.Owner != snapshot.Owner || !validImageID(intent.ImageID) || !ok || !validImageID(digest) || imageRepository(intent.Reference) != imageRepository(image) {
		return intent, errors.New("invalid frozen panel startup intent")
	}
	return intent, nil
}

const bootstrapInspectTemplate = `{"container":` + webInspectTemplate + `,"traefik":{{json (index .Config.Labels "traefik.enable")}},"host_ports":{{json .HostConfig.PortBindings}},"network_mode":{{json .HostConfig.NetworkMode}},"entrypoint":{{json .Config.Entrypoint}},"command":{{json .Config.Cmd}},"privileged":{{json .HostConfig.Privileged}}}`

type bootstrapInspection struct {
	Container webContainer `json:"container"`
	Traefik   string       `json:"traefik"`
	HostPorts map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	} `json:"host_ports"`
	NetworkMode string   `json:"network_mode"`
	Entrypoint  []string `json:"entrypoint"`
	Command     []string `json:"command"`
	Privileged  bool     `json:"privileged"`
}

func (w *installRuntime) inspectBootstrapPanel(ctx context.Context, snapshot installRuntimeSnapshot, intent panelIntent, id string) (webContainer, error) {
	data, err := w.webDocker()(ctx, "inspect", id, "--format", bootstrapInspectTemplate)
	var inspected bootstrapInspection
	c := &inspected.Container
	if err != nil || json.Unmarshal(data, &inspected) != nil || c.ID != id || c.Name != "/"+panelContainerName(snapshot.Owner) || c.ImageID != intent.ImageID || c.Installation != installationOwner(w.root) || c.Operation != snapshot.Owner || inspected.Traefik != "false" || inspected.NetworkMode != "bridge" || inspected.Privileged || !reflect.DeepEqual(inspected.Entrypoint, []string{"/app/x-ui"}) || !reflect.DeepEqual(inspected.Command, []string{"run"}) || len(c.Networks) > 1 {
		return *c, errors.New("isolated panel identity or execution layout changed")
	}
	if c.Running && (len(c.Networks) != 1 || !dockerID(c.Networks["bridge"].NetworkID)) {
		return *c, errors.New("isolated panel network changed")
	}
	for name := range c.Networks {
		if name != "bridge" {
			return *c, errors.New("isolated panel joined an unexpected network")
		}
	}
	if len(c.Mounts) != 2 {
		return *c, errors.New("isolated panel mounts changed")
	}
	want := map[string]struct {
		source string
		rw     bool
	}{"/etc/x-ui": {filepath.Join(w.root, "xui/db"), true}, "/certs": {filepath.Join(w.root, "shared/certs"), false}}
	for _, mount := range c.Mounts {
		expected, ok := want[mount.Destination]
		if !ok || mount.Type != "bind" || mount.Source != expected.source || mount.RW != expected.rw {
			return *c, errors.New("isolated panel mounts changed")
		}
		delete(want, mount.Destination)
	}
	bindings := inspected.HostPorts["2053/tcp"]
	if len(inspected.HostPorts) != 1 || len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" {
		return *c, errors.New("isolated panel host listener changed")
	}
	for port, bindings := range c.Ports {
		for _, binding := range bindings {
			if port != "2053/tcp" || binding.HostIP != "127.0.0.1" || !validBootstrapPort(binding.HostPort) {
				return *c, errors.New("isolated panel has an unexpected published port")
			}
		}
	}
	if c.Running && bootstrapPort(*c) == "" {
		return *c, errors.New("isolated panel listener unavailable")
	}
	return *c, nil
}

func validBootstrapPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == port
}

func bootstrapPort(c webContainer) string {
	bindings := c.Ports["2053/tcp"]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || !validBootstrapPort(bindings[0].HostPort) {
		return ""
	}
	return bindings[0].HostPort
}

type panelIdentity struct {
	webIdentity
	Port      string `json:"port"`
	NetworkID string `json:"network_id"`
}

func identityOfPanel(c webContainer) panelIdentity {
	return panelIdentity{webIdentity{c.ID, c.ImageID, c.StartedAt}, bootstrapPort(c), c.Networks["bridge"].NetworkID}
}

func (w *installRuntime) startPanel(ctx context.Context, dir string, snapshot installRuntimeSnapshot) error {
	var c config
	if json.Unmarshal(snapshot.Binding.Inputs[".stack/config.json"], &c) != nil {
		return errors.New("invalid frozen panel configuration")
	}
	bootstrap, err := newPanelBootstrap(c)
	if err != nil {
		return err
	}
	web, err := w.webIntent(ctx, dir, snapshot)
	if err != nil {
		return err
	}
	intent, err := w.panelIntent(ctx, dir, snapshot, web)
	if err != nil {
		return err
	}
	observe := func() (webContainer, error) {
		found, err := w.observeInstallServices(ctx, snapshot, web, dir, &intent)
		if err != nil {
			return webContainer{}, err
		}
		if !found["decoy"].Running || !found["traefik"].Running {
			return webContainer{}, errors.New("web services are not ready for panel initialization")
		}
		return found[panelContainerName(snapshot.Owner)], nil
	}
	current, err := observe()
	if err != nil {
		return err
	}
	var created webIdentity
	exists, err := readJSON(filepath.Join(dir, "panel-created.json"), &created, true)
	if err != nil {
		return err
	}
	if exists && (created.ID != current.ID || created.ImageID != intent.ImageID) {
		return errors.New("recorded bootstrap panel disappeared or was replaced")
	}
	if current.ID == "" {
		if entries, err := os.ReadDir(filepath.Join(w.root, "xui/db")); err != nil || len(entries) != 0 {
			return errors.New("unstarted panel database is no longer empty")
		}
		_, _ = w.webDocker()(ctx, bootstrapCreateArgs(w.root, snapshot, intent)...)
		current, err = observe()
		if err != nil {
			return err
		}
		if current.ID == "" {
			return errors.New("isolated panel creation did not complete")
		}
	}
	if !exists {
		if current.Running {
			return errors.New("unrecorded bootstrap panel is already running")
		}
		if err := deployment.WriteJSON(filepath.Join(dir, "panel-created.json"), webIdentity{ID: current.ID, ImageID: current.ImageID}); err != nil {
			return err
		}
	}
	var started panelIdentity
	hasStarted, err := readJSON(filepath.Join(dir, "panel-started.json"), &started, true)
	if err != nil {
		return err
	}
	if hasStarted && (started.ID != current.ID || started.ImageID != intent.ImageID || started.StartedAt == "" || !validBootstrapPort(started.Port) || !dockerID(started.NetworkID)) {
		return errors.New("invalid panel startup receipt")
	}
	if !hasStarted {
		if !current.Running {
			if entries, err := os.ReadDir(filepath.Join(w.root, "xui/db")); err != nil || len(entries) != 0 {
				return errors.New("unstarted panel database is no longer empty")
			}
			_, _ = w.webDocker()(ctx, "start", current.ID)
			current, err = observe()
			if err != nil {
				return err
			}
		}
		if !current.Running || current.StartedAt == "" {
			return errors.New("isolated panel has not started")
		}
		started = identityOfPanel(current)
		if err := deployment.WriteJSON(filepath.Join(dir, "panel-started.json"), started); err != nil {
			return err
		}
	}
	var restartFrom, restarted panelIdentity
	hasRestartIntent, err := readJSON(filepath.Join(dir, "panel-restart-intent.json"), &restartFrom, true)
	if err != nil || hasRestartIntent && restartFrom != started {
		return errors.New("invalid panel restart intent")
	}
	hasRestarted, err := readJSON(filepath.Join(dir, "panel-restarted.json"), &restarted, true)
	if err != nil || hasRestarted && (!hasRestartIntent || restarted.ID != started.ID || restarted.ImageID != started.ImageID || restarted.StartedAt == "" || restarted.StartedAt == started.StartedAt || !validBootstrapPort(restarted.Port) || restarted.NetworkID != started.NetworkID) {
		return errors.New("invalid panel restart receipt")
	}
	if !current.Running || current.Networks["bridge"].NetworkID != started.NetworkID || hasRestarted && identityOfPanel(current) != restarted || !hasRestartIntent && identityOfPanel(current) != started {
		return errors.New("bootstrap panel was unexpectedly stopped, restarted or changed")
	}
	endpoint := w.privatePanelEndpoint
	if !hasRestartIntent {
		p, err := endpoint(current, "")
		if err != nil {
			return err
		}
		if w.panelEndpoint == nil {
			if err := waitPanelHTTP(ctx, p); err != nil {
				p.transport.CloseIdleConnections()
				return err
			}
			observed, err := observe()
			if err != nil || identityOfPanel(observed) != started {
				p.transport.CloseIdleConnections()
				return errors.New("bootstrap panel changed before initialization")
			}
		}
		err = bootstrap.prepare(ctx, p)
		p.transport.CloseIdleConnections()
		if err != nil {
			return err
		}
		current, err = observe()
		if err != nil || identityOfPanel(current) != started {
			return errors.New("bootstrap panel changed during initialization")
		}
		if err := deployment.WriteJSON(filepath.Join(dir, "panel-restart-intent.json"), started); err != nil {
			return err
		}
	}
	if !hasRestarted {
		if identityOfPanel(current) == started {
			_, _ = w.webDocker()(ctx, "restart", current.ID)
			current, err = observe()
			if err != nil {
				return err
			}
		}
		if !current.Running || current.ID != started.ID || current.StartedAt == "" || current.StartedAt == started.StartedAt {
			return errors.New("initialized panel restart has not completed")
		}
		restarted = identityOfPanel(current)
		if err := deployment.WriteJSON(filepath.Join(dir, "panel-restarted.json"), restarted); err != nil {
			return err
		}
	}
	p, err := endpoint(current, c.Env["PANEL_PATH"])
	if err != nil {
		return err
	}
	if w.panelEndpoint == nil {
		if err := waitPanelHTTP(ctx, p); err != nil {
			p.transport.CloseIdleConnections()
			return err
		}
	}
	err = bootstrap.verify(ctx, p)
	p.transport.CloseIdleConnections()
	if err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Join(w.root, "xui/db/x-ui.db")); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("initialized panel database is not persisted")
	}
	current, err = observe()
	if err != nil || identityOfPanel(current) != restarted {
		return errors.New("initialized panel changed during readiness verification")
	}
	return nil
}

func (w *installRuntime) privatePanelEndpoint(current webContainer, prefix string) (*Panel, error) {
	if w.panelEndpoint != nil {
		return w.panelEndpoint(current, prefix)
	}
	return NewPanel("http://" + net.JoinHostPort("127.0.0.1", bootstrapPort(current)) + strings.TrimRight(prefix, "/"))
}

func (w *installRuntime) provisionPanel(ctx context.Context, dir string, snapshot installRuntimeSnapshot) error {
	var c config
	if json.Unmarshal(snapshot.Binding.Inputs[".stack/config.json"], &c) != nil {
		return errors.New("invalid frozen provisioning configuration")
	}
	web, err := w.webIntent(ctx, dir, snapshot)
	if err != nil {
		return err
	}
	var intent panelIntent
	if ok, err := readJSON(filepath.Join(dir, "panel-intent.json"), &intent, true); err != nil || !ok {
		return errors.New("panel startup intent is unavailable")
	}
	intent, err = w.panelIntent(ctx, dir, snapshot, web)
	if err != nil {
		return err
	}
	var saved panelIdentity
	if ok, err := readJSON(filepath.Join(dir, "panel-restarted.json"), &saved, true); err != nil || !ok {
		return errors.New("panel initialization receipt is unavailable")
	}
	observe := func() (webContainer, error) {
		found, err := w.observeInstallServices(ctx, snapshot, web, dir, &intent)
		if err != nil {
			return webContainer{}, err
		}
		current := found[panelContainerName(snapshot.Owner)]
		if !current.Running || identityOfPanel(current) != saved {
			return webContainer{}, errors.New("initialized provisioning target changed")
		}
		return current, nil
	}
	current, err := observe()
	if err != nil {
		return err
	}
	panel, err := w.privatePanelEndpoint(current, c.Env["PANEL_PATH"])
	if err != nil {
		return err
	}
	defer panel.transport.CloseIdleConnections()
	session, err := panel.login(ctx, c.Env["PANEL_USER"], c.Env["PANEL_PASS"])
	if err != nil {
		return err
	}
	return provisionFreshPanel(ctx, dir, c, session, func() error { _, err := observe(); return err })
}

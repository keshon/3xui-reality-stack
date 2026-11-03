package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type dockerCommand func(context.Context, ...string) ([]byte, error)

type container struct {
	ID        string `json:"id"`
	Image     string `json:"image"`
	ImageID   string `json:"image_id"`
	Running   bool   `json:"running"`
	StartedAt string `json:"started_at"`
	Mounts    []struct {
		Type, Source, Destination string
		RW                        bool
	} `json:"mounts"`
	Ports map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string
	} `json:"ports"`
}

// Select only necessary metadata. Config.Env and labels may contain credentials.
const inspectTemplate = `{"id":{{json .Id}},"image":{{json .Config.Image}},"image_id":{{json .Image}},"running":{{json .State.Running}},"started_at":{{json .State.StartedAt}},"mounts":{{json .Mounts}},"ports":{{json .NetworkSettings.Ports}}}`

type runtimeObservation struct {
	Panel        container `json:"panel"`
	Traefik      container `json:"traefik"`
	PanelVersion string    `json:"panel_version"`
}

func dockerID(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 1<<20 {
		return 0, errors.New("Docker output exceeded limit")
	}
	return b.Buffer.Write(data)
}

func localDocker(ctx context.Context, args ...string) ([]byte, error) {
	return localDockerTimeout(ctx, 10*time.Second, args...)
}

func localDockerTimeout(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("runtime discovery requires the VPS's local Linux Docker socket")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Ignore DOCKER_HOST and CLI contexts, which may point at a different server.
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if cmd.Run() != nil {
		return nil, errors.New("local Docker inspection failed")
	}
	return output.Bytes(), nil
}

// Runtime checks the existing installation without login, provisioning or
// Compose execution. Passing these checks does not prove transport health.
func (s Stack) Runtime(ctx context.Context, root string) (deployment.RuntimeReport, error) {
	report, _, err := s.observeRuntime(ctx, root)
	return report, err
}

func (s Stack) observeRuntime(ctx context.Context, root string) (deployment.RuntimeReport, runtimeObservation, error) {
	i, err := Inspect(root)
	if err != nil || !i.Report.Managed || !i.Report.Baseline || len(i.Report.Blockers) != 0 {
		return deployment.RuntimeReport{Stack: stackID, Status: "blocked", Checks: []deployment.Check{{ID: "managed-state", Reason: "managed state is absent, invalid, changed or awaiting recovery"}}}, runtimeObservation{}, ctx.Err()
	}
	return s.observeAppliedRuntime(ctx, root, i.applied)
}

// observeAppliedRuntime is also used during recovery, when local outputs may
// already contain this operation's targets. Its caller must validate local
// ownership separately before supplying the frozen applied configuration.
func (s Stack) observeAppliedRuntime(ctx context.Context, root string, applied config) (deployment.RuntimeReport, runtimeObservation, error) {
	var observation runtimeObservation
	report := deployment.RuntimeReport{Stack: stackID, Status: "compatible", Checks: []deployment.Check{}, Observation: "local Docker metadata and panel binary version only; image provenance, API authentication and tunnel health not verified"}
	add := func(id string, ok bool, reason string) {
		check := deployment.Check{ID: id, OK: ok}
		if !ok {
			check.Reason, report.Status = reason, "blocked"
		}
		report.Checks = append(report.Checks, check)
	}
	if err := ctx.Err(); err != nil {
		return report, observation, err
	}
	add("managed-state", true, "")
	var policy struct {
		Schema       int    `json:"schema"`
		PanelImage   string `json:"panel_image"`
		TraefikImage string `json:"traefik_image"`
	}
	exists, err := readJSON(filepath.Join(root, "compatibility.json"), &policy, false)
	// A policy edit cannot make an untested panel API version supported.
	if err != nil || !exists || policy.Schema != 1 || policy.PanelImage != "ghcr.io/mhsanaei/3x-ui:v3.7.0" || policy.TraefikImage == "" {
		add("compatibility-policy", false, "compatibility policy is missing or outside the tested panel API boundary")
		return report, observation, nil
	}
	add("compatibility-policy", true, "")
	run := s.docker
	if run == nil {
		run = localDocker
	}
	inspect := func(name string) (container, bool) {
		data, err := run(ctx, "inspect", "--type", "container", "--format", inspectTemplate, name)
		var c container
		if err != nil || json.Unmarshal(data, &c) != nil || !dockerID(c.ID) || !strings.HasPrefix(c.ImageID, "sha256:") || !dockerID(strings.TrimPrefix(c.ImageID, "sha256:")) {
			return c, false
		}
		if _, err := time.Parse(time.RFC3339Nano, c.StartedAt); err != nil {
			return c, false
		}
		return c, true
	}
	panel, panelOK := inspect("3x-ui")
	observation.Panel = panel
	add("panel-container", panelOK && panel.Running, "3x-ui container is unavailable, stopped or cannot be inspected")
	add("panel-image", panelOK && panel.Image == policy.PanelImage, "panel image reference differs from the pinned policy")
	expected, pathErr := filepath.EvalSymlinks(filepath.Join(root, "xui", "db"))
	matching, total := false, 0
	for _, mount := range panel.Mounts {
		if strings.TrimRight(mount.Destination, "/") != "/etc/x-ui" {
			continue
		}
		total++
		source, err := filepath.EvalSymlinks(mount.Source)
		matching = pathErr == nil && err == nil && source == expected && mount.Type == "bind" && mount.RW
	}
	add("database-ownership", panelOK && total == 1 && matching, "panel database is not the writable bind mount owned by this checkout")
	if panelOK && panel.Running && panel.Image == policy.PanelImage && total == 1 && matching {
		// Use the inspected immutable ID, not a name that could be reassigned.
		data, err := run(ctx, "exec", panel.ID, "/app/x-ui", "-v")
		observation.PanelVersion = strings.TrimSpace(string(data))
		add("panel-version", err == nil && strings.TrimPrefix(strings.TrimSpace(string(data)), "v") == "3.7.0", "panel binary version differs from the tested API version")
	} else {
		add("panel-version", false, "panel binary version cannot be checked before confirming container ownership and image")
	}
	traefik, traefikOK := inspect("traefik")
	observation.Traefik = traefik
	add("traefik-container", traefikOK && traefik.Running, "Traefik container is unavailable, stopped or cannot be inspected")
	add("traefik-image", traefikOK && traefik.Image == policy.TraefikImage, "Traefik image reference differs from the pinned policy")
	bind := applied.Env["MANAGEMENT_BIND"]
	if bind == "" {
		bind = "127.0.0.1"
		if applied.Env["ROLE"] == "node" {
			bind = "0.0.0.0"
		}
	}
	port := applied.Env["MANAGEMENT_PORT"]
	if port == "" {
		port = "2053"
	}
	parsedPort, err := strconv.Atoi(port)
	ports := traefik.Ports["8443/tcp"]
	management := err == nil && parsedPort > 0 && parsedPort <= 65535 && len(ports) == 1 && ports[0].HostIP == bind && ports[0].HostPort == port
	add("management-listener", traefikOK && management, "independent management HTTPS binding differs from the applied configuration")
	if err := ctx.Err(); err != nil {
		return report, observation, err
	}
	return report, observation, nil
}

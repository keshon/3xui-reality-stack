package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

type webDockerFixture struct {
	*runtimeDockerFixture
	t                  *testing.T
	root               string
	containers         map[string]webContainer
	starts, imageReads int
	tagMoved           bool
}

// Web-stage tests deliberately stop before any panel effects.
type webStageBoundary struct{ *installRuntime }

func (w webStageBoundary) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	steps, err := w.installRuntime.Steps(ctx, dir)
	for i := range steps {
		if steps[i].ID == "panel" {
			steps[i].Run = func(context.Context) error { return deployment.ErrExecutionUnavailable }
		}
	}
	return steps, err
}

func (f *webDockerFixture) run(ctx context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "image":
		f.imageReads++
		digit := "d"
		if strings.Contains(args[2], "traefik") {
			digit = "e"
		}
		if f.tagMoved {
			digit = "f"
		}
		repository := "nginx"
		if strings.Contains(args[2], "traefik") {
			repository = "traefik"
		}
		return json.Marshal(map[string]any{"id": "sha256:" + strings.Repeat(digit, 64), "digests": []string{repository + "@sha256:" + strings.Repeat("9", 64)}})
	case "ps":
		var data bytes.Buffer
		for name, current := range f.containers {
			row, _ := json.Marshal(map[string]string{"id": current.ID, "name": name})
			data.Write(row)
			data.WriteByte('\n')
		}
		return data.Bytes(), nil
	case "inspect":
		for _, current := range f.containers {
			if current.ID == args[1] {
				return json.Marshal(current)
			}
		}
		return nil, errors.New("unknown web container identity")
	case "compose":
		if args[len(args)-1] == "--quiet" {
			return f.runtimeDockerFixture.run(ctx, args...)
		}
		project := args[len(args)-1]
		if project != "decoy" && project != "traefik" {
			f.t.Fatal("panel was started before provisioning")
		}
		var overlay string
		for i := range args {
			if args[i] == "-f" {
				overlay = args[i+1]
			}
		}
		var override struct {
			Services map[string]struct {
				Image  string
				Labels map[string]string
			}
		}
		ok, err := readJSON(overlay, &override, false)
		if err != nil || !ok {
			f.t.Fatal("missing frozen startup override")
		}
		service := override.Services[project]
		_, digest, ok := strings.Cut(service.Image, "@")
		if !ok || !validImageID(digest) || !strings.Contains(strings.Join(args, " "), "--no-recreate --pull never") {
			f.t.Fatal("startup did not freeze images or prevent recreation")
		}
		var doc composeDocument
		readJSON(filepath.Join(f.root, project, "compose.json"), &doc, false)
		current := webContainer{Name: "/" + project, Project: project, Service: project, Installation: service.Labels["io.stackctl.installation"], Operation: service.Labels["io.stackctl.operation"]}
		digit := "b"
		if project == "traefik" {
			digit = "c"
		}
		current.ID = strings.Repeat(digit, 64)
		imageDigit := "d"
		if project == "traefik" {
			imageDigit = "e"
		}
		current.ImageID = "sha256:" + strings.Repeat(imageDigit, 64)
		current.Running = true
		current.StartedAt = "2026-11-03T10:00:00Z"
		current.Networks = map[string]struct{ NetworkID string }{"proxy": {NetworkID: f.network.ID}}
		for _, volume := range doc.Services[project].Volumes {
			parts := strings.Split(volume, ":")
			source := parts[0]
			if !path.IsAbs(source) {
				source = filepath.Clean(filepath.Join(f.root, project, source))
			}
			current.Mounts = append(current.Mounts, struct {
				Type, Source, Destination string
				RW                        bool
			}{"bind", source, parts[1], len(parts) < 3 || parts[2] != "ro"})
		}
		if project == "traefik" {
			json.Unmarshal([]byte(`{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}],"8443/tcp":[{"HostIp":"127.0.0.1","HostPort":"2053"}]}`), &current.Ports)
			os.WriteFile(filepath.Join(f.root, acmeStateFile), []byte(`{"simulated":"ACME runtime state"}`), 0600)
			os.WriteFile(filepath.Join(f.root, "traefik/data/logs/traefik.log"), []byte("runtime log"), 0600)
		}
		f.containers[project] = current
		f.starts++
		if f.network.Endpoints == nil {
			f.network.Endpoints = map[string]json.RawMessage{}
		}
		f.network.Endpoints[current.ID] = json.RawMessage(`{}`)
		f.network.Containers = len(f.network.Endpoints)
		return nil, errors.New("synthetic lost Compose startup reply")
	}
	return f.runtimeDockerFixture.run(ctx, args...)
}

func webWorkflowFixture(t *testing.T) (*installRuntime, *webDockerFixture, *runtimeHelperFixture) {
	t.Helper()
	workflow, docker, helper := runtimeWorkflowFixture(t)
	web := &webDockerFixture{runtimeDockerFixture: docker, t: t, root: workflow.root, containers: map[string]webContainer{}}
	workflow.stack.docker = web.run
	workflow.stack.preflightHost = func(ctx context.Context, ports []deployment.Port) (host.Observation, error) {
		observed, err := clearPreflightHost(ctx, ports)
		if docker.network != nil {
			observed.Facts.CustomNetworks = 1
		}
		observed.Facts.Containers = len(web.containers)
		observed.Facts.ComposeProjects = len(web.containers)
		observed.Checks = []deployment.Check{{ID: "platform", OK: true}, {ID: "existing-networks", OK: docker.network == nil}, {ID: "existing-containers", OK: len(web.containers) == 0}}
		if web.containers["traefik"].Running {
			observed.Checks = append(observed.Checks, deployment.Check{ID: "port-tcp-80", OK: false}, deployment.Check{ID: "port-tcp-2053", OK: false})
		}
		return observed, err
	}
	workflow.webReady = func(context.Context, string, map[string][]byte) error { return nil }
	return workflow, web, helper
}

func TestWebStartupRecoversLostReplyAndCheckpointWithoutStartingPanel(t *testing.T) {
	workflow, docker, helper := webWorkflowFixture(t)
	engine := deployment.Engine{Root: workflow.root}
	if err := engine.Start(t.Context(), &runtimeFailure{installRuntime: workflow, after: "services"}); err == nil {
		t.Fatal("expected lost service checkpoint")
	}
	if docker.starts != 2 || helper.activations != 1 {
		t.Fatal("web bootstrap did not start exactly its two services")
	}
	docker.tagMoved = true
	resumed := &installRuntime{root: workflow.root, stack: workflow.stack, helper: helper, webReady: workflow.webReady}
	if err := engine.Resume(t.Context(), webStageBoundary{resumed}); err == nil || !strings.Contains(err.Error(), "panel") {
		t.Fatal("web recovery did not stop at pending panel provisioning")
	}
	if docker.starts != 2 || docker.imageReads != 2 || docker.creates != 1 {
		t.Fatal("web recovery recreated resources or followed changed image tags")
	}
	entries, err := os.ReadDir(filepath.Join(workflow.root, "xui/db"))
	if err != nil || len(entries) != 0 {
		t.Fatal("web bootstrap touched panel state")
	}
	if _, err := os.Stat(filepath.Join(workflow.root, ".stack/applied.json")); !os.IsNotExist(err) {
		t.Fatal("partial installation committed applied state")
	}
}

func TestWebCertificateWaitResumesWithoutRestartingContainers(t *testing.T) {
	workflow, docker, helper := webWorkflowFixture(t)
	pending := true
	workflow.webReady = func(context.Context, string, map[string][]byte) error {
		if pending {
			return errors.New("certificate issuance pending")
		}
		return nil
	}
	engine := deployment.Engine{Root: workflow.root}
	if err := engine.Start(t.Context(), workflow); err == nil || !strings.Contains(err.Error(), "services") {
		t.Fatal("pending certificates were accepted")
	}
	pending = false
	resumed := &installRuntime{root: workflow.root, stack: workflow.stack, helper: helper, webReady: workflow.webReady}
	if err := engine.Resume(t.Context(), webStageBoundary{resumed}); err == nil || !strings.Contains(err.Error(), "panel") {
		t.Fatal("certificate wait did not resume to panel boundary")
	}
	if docker.starts != 2 || helper.activations != 1 {
		t.Fatal("certificate wait restarted services")
	}
}

func TestWebRecoveryRejectsForeignContainersReplacementAndMountDrift(t *testing.T) {
	for _, fault := range []string{"foreign", "replaced", "mount", "restart", "source"} {
		t.Run(fault, func(t *testing.T) {
			workflow, docker, helper := webWorkflowFixture(t)
			workflow.webReady = func(context.Context, string, map[string][]byte) error { return errors.New("pending") }
			engine := deployment.Engine{Root: workflow.root}
			if err := engine.Start(t.Context(), workflow); err == nil {
				t.Fatal("expected certificate wait")
			}
			current := docker.containers["decoy"]
			switch fault {
			case "foreign":
				docker.containers["foreign-app"] = webContainer{container: container{ID: strings.Repeat("f", 64)}}
			case "replaced":
				current.ID = strings.Repeat("f", 64)
			case "mount":
				current.Mounts[0].Source = "/foreign/site"
			case "restart":
				current.StartedAt = "2026-11-03T10:01:00Z"
			case "source":
				os.WriteFile(filepath.Join(workflow.root, "decoy/site/index.html"), []byte("unreviewed page"), 0644)
			}
			docker.containers["decoy"] = current
			resumed := &installRuntime{root: workflow.root, stack: workflow.stack, helper: helper, webReady: func(context.Context, string, map[string][]byte) error {
				t.Fatal("invalid resources reached certificate readiness")
				return nil
			}}
			if err := engine.Resume(t.Context(), resumed); err == nil {
				t.Fatal("changed web deployment was accepted")
			}
			if docker.starts != 2 || helper.activations != 1 {
				t.Fatal("conflict triggered service recreation")
			}
		})
	}
}

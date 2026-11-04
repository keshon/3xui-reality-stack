package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

type controllerObservation struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}

type installHostObservation struct {
	Controller   controllerObservation   `json:"controller"`
	Service      serviceFile             `json:"service"`
	ServiceState host.ServiceObservation `json:"service_state"`
}

type installBinding struct {
	Inputs map[string][]byte      `json:"inputs"`
	Host   deployment.HostFacts   `json:"host"`
	Helper installHostObservation `json:"helper"`
}

func installPlan() deployment.Plan {
	return deployment.Plan{Stack: stackID, Status: "proposed", ReadOnly: true,
		Reasons: []string{"standalone installation; requires a reviewed, conflict-free host and an installed release controller"},
		Actions: []deployment.Action{
			{ID: "snapshot", Resource: "installation-inputs", Effects: []string{"freeze draft, controller release, host prerequisites and helper service ownership"}},
			{ID: "runtime", Resource: "docker", Effects: []string{"revalidate host conflicts; create the reviewed proxy network and validate generated Compose files"}},
			{ID: "certificates", Resource: "certificate-export", Effects: []string{"install and enable the reviewed exporter unit; resolve pinned web images to immutable digests; start Traefik and the decoy; obtain and verify certificates"}},
			{ID: "panel", Resource: "panel", Effects: []string{"start the pinned panel locally; configure administrator access, transports and unified accounts"}},
			{ID: "verify", Resource: "stack", Effects: []string{"preserve recovery snapshots and promote the configured panel to reviewed public ports and Traefik routes", "verify subscription contents and authenticated transfers"}},
			{ID: "commit", Resource: "managed-state", Effects: []string{"commit applied state only after service and transfer verification"}},
		}}
}

func (s Stack) reviewInstall(ctx context.Context, root string) (deployment.ReviewSpec, error) {
	_, before, err := readDraft(root)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	var c config
	if json.Unmarshal(before[".stack/config.json"], &c) != nil {
		return deployment.ReviewSpec{}, errors.New("invalid fresh-install configuration")
	}
	expected, err := initialArtifacts(c)
	if err != nil {
		return deployment.ReviewSpec{}, err
	}
	for name, data := range expected {
		if !bytes.Equal(data, before[name]) {
			return deployment.ReviewSpec{}, errors.New("fresh-install outputs do not match this controller's configuration and assets")
		}
	}
	report, err := s.Preflight(ctx, root)
	if err != nil || report.Installation != "draft" || report.Status != "candidate" {
		return deployment.ReviewSpec{}, errors.New("fresh-install host has unresolved prerequisite or resource conflicts")
	}
	observe := s.installHost
	if observe == nil {
		observe = observeInstallHost
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return deployment.ReviewSpec{}, errors.New("cannot resolve fresh-install root")
	}
	physical, err = filepath.Abs(physical)
	if err != nil {
		return deployment.ReviewSpec{}, errors.New("cannot resolve absolute fresh-install root")
	}
	helper, err := observe(ctx, physical, c.Env["CERT_BASENAME"])
	if err != nil || !helper.ServiceState.Absent() {
		return deployment.ReviewSpec{}, errors.New("controller release or certificate helper ownership is unavailable or conflicts with an existing service")
	}
	_, after, err := readDraft(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		return deployment.ReviewSpec{}, errors.New("fresh-install draft changed during review")
	}
	if err := ctx.Err(); err != nil {
		return deployment.ReviewSpec{}, err
	}
	data, err := json.Marshal(installBinding{Inputs: before, Host: report.Host, Helper: helper})
	return deployment.ReviewSpec{Kind: "xui-install-v1", Plan: installPlan(), Binding: data}, err
}

func observeInstallHost(ctx context.Context, root, domain string) (installHostObservation, error) {
	controller, err := currentController(ctx)
	if err != nil {
		return installHostObservation{}, err
	}
	service, err := certificateService(root, controller.Path, domain)
	if err != nil {
		return installHostObservation{}, err
	}
	state, err := host.ObserveService(ctx, service.Name)
	if err != nil {
		return installHostObservation{}, err
	}
	return installHostObservation{Controller: controller, Service: service, ServiceState: state}, nil
}

package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/xui"
)

type wizardBackend struct {
	fakeBackend
	draft, blocked, failReview bool
	initializations            int
	input                      json.RawMessage
}

func (b *wizardBackend) InstallationTarget() string { return "/opt/new-stack" }
func (b *wizardBackend) Methods() []string {
	return append(b.fakeBackend.Methods(), "initialize", "preflight")
}
func (b *wizardBackend) Inspect(context.Context) (deployment.Report, error) {
	return deployment.Report{Stack: "fixture", Managed: b.draft}, nil
}
func (b *wizardBackend) Initialize(_ context.Context, input json.RawMessage) (deployment.InitializationReport, error) {
	b.initializations++
	b.input = append(json.RawMessage(nil), input...)
	b.draft = true
	return deployment.InitializationReport{Status: "prepared"}, nil
}
func (b *wizardBackend) Preflight(context.Context) (deployment.PreflightReport, error) {
	state := "candidate"
	if b.blocked {
		state = "blocked"
	}
	return deployment.PreflightReport{Status: state, Installation: "draft", Checks: []deployment.Check{{ID: "existing-containers", OK: !b.blocked, Reason: "Existing containers require manual isolation."}}}, nil
}
func (b *wizardBackend) Review(ctx context.Context) (deployment.Review, error) {
	if b.failReview {
		return deployment.Review{}, errors.New("review unavailable")
	}
	r, err := b.fakeBackend.Review(ctx)
	r.Kind = "xui-install-v1"
	return r, err
}

func newWizardModel(t *testing.T) (*model, *wizardBackend) {
	t.Helper()
	b := &wizardBackend{fakeBackend: fakeBackend{writable: true, p: deployment.Plan{Stack: "fixture", Status: "proposed", ReadOnly: true, Actions: []deployment.Action{{ID: "install", Resource: "stack"}}}}}
	m := newModel(t.Context(), b, "Install fixture")
	_, _ = m.Update(m.Init()())
	if m.screen != installationForm {
		t.Fatal("new target did not open wizard")
	}
	return m, b
}

func fillWizard(t *testing.T, m *model, mode string) {
	t.Helper()
	values := map[int]string{0: "example.com", 1: "www.example.com", 2: "ops.example.com", 3: "admin@example.com", 4: mode, 5: "private-dns-token", 6: "alice, bob", 7: "admin", 8: "private-panel-password", 9: "private-panel-password"}
	for m.wizard.step < len(wizardFields) {
		step := m.wizard.step
		m.wizard.fields[step].SetValue(values[step])
		if wizardFields[step].secret && strings.Contains(ansi.Strip(m.View().Content), values[step]) {
			t.Fatal("wizard displayed secret")
		}
		m.wizardNext()
		if m.wizard.step == step {
			t.Fatalf("wizard did not advance from step %d", step)
		}
	}
	view := ansi.Strip(m.View().Content)
	if strings.Contains(view, "private-dns-token") || strings.Contains(view, "private-panel-password") {
		t.Fatal("summary displayed credentials")
	}
}

func TestInstallationWizardBlockedHostAndExplicitDeployment(t *testing.T) {
	m, b := newWizardModel(t)
	b.blocked = true
	fillWizard(t, m, "http")
	finish(m, m.wizardNext())
	if b.initializations != 1 || m.screen != installationChecks || b.applyCalls != 0 || len(m.wizard.fields) != 0 {
		t.Fatal("preparation deployed, lost draft or retained secret fields")
	}
	var input map[string]any
	if json.Unmarshal(b.input, &input) != nil || input["dns_token"] != nil || len(input["users"].([]any)) != 2 {
		t.Fatal("HTTP form serialization lost users or included hidden DNS credentials")
	}
	if !strings.Contains(m.content(), "Stopped containers") {
		t.Fatal("conflict recovery omitted stopped container boundary")
	}
	b.blocked = false
	finish(m, m.prepareInstallation(nil))
	if m.screen != review || b.initializations != 1 || b.applyCalls != 0 {
		t.Fatal("recheck recreated draft or started deployment")
	}
	m.dispatch("apply")
	if m.screen != confirmation || !strings.Contains(ansi.Strip(m.View().Content), "Deploy") {
		t.Fatal("installation lacks separate deployment confirmation")
	}
	press(m, "esc")
	if b.applyCalls != 0 {
		t.Fatal("cancelled confirmation deployed")
	}
	m.dispatch("apply")
	cmd := press(m, "enter")
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("deployment did not start operation progress polling")
	}
	finish(m, batch[0])
	if b.applyCalls != 1 {
		t.Fatal("confirmed deployment did not run exactly once")
	}
}

func TestInstallationWizardCancellationAndPostInitializationReviewFailure(t *testing.T) {
	m, b := newWizardModel(t)
	fillWizard(t, m, "dns")
	press(m, "esc")
	if len(m.wizard.fields) != 0 || b.initializations != 0 || b.applyCalls != 0 {
		t.Fatal("cancelled form retained fields or caused effects")
	}
	m.startWizard()
	fillWizard(t, m, "dns")
	b.failReview = true
	finish(m, m.wizardNext())
	if m.screen != installationChecks || len(m.wizard.fields) != 0 || b.applyCalls != 0 {
		t.Fatal("failed review did not retain safe draft recovery")
	}
	_ = m.View() // Regression: a cleared form must never render nil input fields.
	if !strings.Contains(m.message, "review") {
		t.Fatal("review failure was hidden")
	}
	b.failReview = false
	finish(m, m.prepareInstallation(nil))
	if b.initializations != 1 || m.screen != review {
		t.Fatal("existing draft did not reopen without reinitialization")
	}
}

func TestInstallationWizardInterruptClearsFieldsBeforeFinalRender(t *testing.T) {
	m, b := newWizardModel(t)
	m.wizard.fields[8].SetValue("private-interrupted-password")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil || len(m.wizard.fields) != 0 || m.screen == installationForm || b.initializations != 0 {
		t.Fatal("interrupt retained active secret form")
	}
	_ = m.View()
}

func TestInstallationWizardFramesAndMouseControls(t *testing.T) {
	for _, size := range [][2]int{{40, 18}, {64, 24}, {80, 24}, {120, 40}} {
		m, _ := newWizardModel(t)
		m.resize(size[0], size[1])
		for step := 0; step <= len(wizardFields); step++ {
			m.wizard.step = step
			view, hits := m.compose()
			plain := ansi.Strip(view.Content)
			if !strings.Contains(plain, "Cancel") || !strings.Contains(plain, "Back") {
				t.Fatalf("wizard buttons clipped at %v step %d", size, step)
			}
			for _, h := range hits {
				if h.x < 0 || h.y < 0 || h.x+h.width > size[0] || h.y+h.height > size[1] {
					t.Fatalf("wizard hit escaped frame at %v", size)
				}
			}
		}
		m.wizard.step = 0
		m.wizard.fields[0].SetValue("example.com")
		_, hits := m.compose()
		for _, h := range hits {
			if h.action == "wizard-next" {
				m.mouse(tea.MouseClickMsg{Button: tea.MouseLeft, X: h.x, Y: h.y})
				break
			}
		}
		if m.wizard.step != 1 {
			t.Fatal("wizard Next mouse button did not advance")
		}
	}
}

func TestInstallationProgressKeepsExactRecoveryAndStopsPolling(t *testing.T) {
	m, _ := newWizardModel(t)
	m.clearWizard()
	m.navigate(operation)
	m.busy, m.mutating = true, true
	op := deployment.OperationStatus{ID: strings.Repeat("b", 32), Kind: "xui-install-v1", Status: "running", Completed: 4, Total: 7, CurrentStep: "panel"}
	_, cmd := m.Update(operationProgress{op})
	if cmd == nil || m.state.operation.ID != op.ID || !strings.Contains(m.content(), "4 / 7") {
		t.Fatal("progress failed to display saved operation")
	}
	m.busy, m.mutating = false, false
	_, cmd = m.Update(operationProgress{op})
	if cmd != nil {
		t.Fatal("completed request kept progress polling alive")
	}
}

func TestWizardPreparesRealControllerDraftWithoutDeploying(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new-stack")
	c := control.Controller{Root: root, Stack: xui.InstallStack{}}
	m := newModel(t.Context(), NewInstallationBackend(c), "Real controller draft")
	_, _ = m.Update(m.Init()())
	if m.screen != installationForm {
		t.Fatal("new real target did not open setup")
	}
	fillWizard(t, m, "http")
	finish(m, m.wizardNext())
	if _, err := os.Stat(filepath.Join(root, ".stack/config.json")); err != nil {
		t.Fatal("wizard request was not accepted by real initializer", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".stack/applied.json")); !os.IsNotExist(err) {
		t.Fatal("preparation published a deployment")
	}
	if len(m.wizard.fields) != 0 {
		t.Fatal("real initialization retained secret fields")
	}
	op, err := c.Operation(t.Context())
	if err != nil || op.Status != "idle" {
		t.Fatal("preparation started an operation")
	}
}

func TestUnfinishedInstallationOpensRecoveryInsteadOfNewForm(t *testing.T) {
	m, b := newWizardModel(t)
	b.draft = true
	m.clearWizard()
	m.navigate(home)
	b.op = deployment.OperationStatus{ID: strings.Repeat("b", 32), Kind: "xui-install-v1", Status: "failed", Completed: 3, Total: 7}
	_, _ = m.Update(m.refresh()())
	if m.screen != operation || !m.available("resume") || m.available("install") {
		t.Fatal("unfinished operation did not reopen exact recovery")
	}
}

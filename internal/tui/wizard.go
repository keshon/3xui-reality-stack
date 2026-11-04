package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// The local executable explicitly supplies installation capability. A remote
// controller advertising initialization alone must not enable installation UI.
type installationBackend interface {
	control.Initializer
	control.Preflighter
	InstallationTarget() string
}

type localInstallation struct{ control.Controller }

func (b localInstallation) InstallationTarget() string { return b.Root }

func NewInstallationBackend(controller control.Controller) Backend {
	return localInstallation{controller}
}

type wizardField struct {
	key, label, hint string
	secret           bool
}

var wizardFields = []wizardField{
	{"base_domain", "Base domain", "Example: example.com", false},
	{"public_domain", "Public website (decoy) hostname", "DNS must point directly at this VPS.", false},
	{"panel_domain", "Panel hostname", "DNS must point directly at this VPS.", false},
	{"acme_email", "Certificate email", "An email address for ACME registration.", false},
	{"acme_mode", "Certificate validation", "http: validate on port 80. dns: use a Cloudflare API token.", false},
	{"dns_token", "Cloudflare DNS API token", "Masked. Zone DNS Edit + Zone Read. Paste via your terminal (ConEmu: Shift+Insert).", true},
	{"users", "Initial VPN users", "Unique names: a-z, 0-9, underscore; 1-24 characters. Separate with spaces or commas.", false},
	{"panel_user", "Panel admin username", "Leave empty to generate an admin username.", false},
	{"panel_password", "Panel password", "Leave empty to generate a strong password.", true},
	{"repeat_password", "Repeat panel password", "Leave empty when using a generated password.", true},
}

type installationWizard struct {
	fields      []textinput.Model
	step, focus int
}

func (m *model) installer() installationBackend {
	b, _ := m.backend.(installationBackend)
	if b == nil || b.InstallationTarget() == "" || !m.supports("initialize", "preflight", "apply") {
		return nil
	}
	return b
}

func (m *model) clearWizard() {
	for i := range m.wizard.fields {
		m.wizard.fields[i].Reset()
		m.wizard.fields[i].Blur()
	}
	m.wizard = installationWizard{}
	if m.screen == installationForm {
		m.screen = home
	}
}

func (m *model) startWizard() tea.Cmd {
	m.clearWizard()
	for _, field := range wizardFields {
		input := textinput.New()
		input.CharLimit = 4096
		input.SetWidth(max(1, min(m.width-16, 48)))
		if field.secret {
			input.EchoMode = textinput.EchoPassword
		}
		m.wizard.fields = append(m.wizard.fields, input)
	}
	m.wizard.fields[4].SetValue("http")
	m.modalBase = home
	m.screen = installationForm
	m.message = "Tab chooses controls · Enter continues · Esc cancels"
	return m.wizard.fields[0].Focus()
}

func (m *model) wizardMove(delta int) tea.Cmd {
	w := &m.wizard
	if w.step < len(w.fields) {
		w.fields[w.step].Blur()
	}
	w.step += delta
	if w.step == 5 && w.fields[4].Value() != "dns" {
		w.step += delta
	}
	w.focus = 0
	if w.step < len(w.fields) {
		return w.fields[w.step].Focus()
	}
	return nil
}

func (m *model) wizardNext() tea.Cmd {
	w := &m.wizard
	if w.step == len(w.fields) {
		input := map[string]any{"schema_version": 1}
		for i, field := range wizardFields {
			if field.key == "repeat_password" {
				continue
			}
			value := w.fields[i].Value()
			if field.key == "users" {
				input[field.key] = strings.Fields(strings.ReplaceAll(value, ",", " "))
			} else if value != "" && !(field.key == "dns_token" && w.fields[4].Value() != "dns") {
				input[field.key] = value
			}
		}
		raw, _ := json.Marshal(input)
		return m.prepareInstallation(raw)
	}
	value := w.fields[w.step].Value()
	if w.step <= 6 && strings.TrimSpace(value) == "" {
		m.message = "This field is required."
		return nil
	}
	if w.step == 4 && value != "http" && value != "dns" {
		m.message = "Choose http or dns."
		return nil
	}
	if w.step == 9 && value != w.fields[8].Value() {
		m.message = "Passwords must match."
		return nil
	}
	if w.step == 0 {
		if w.fields[1].Value() == "" {
			w.fields[1].SetValue("www." + value)
		}
		if w.fields[2].Value() == "" {
			w.fields[2].SetValue("ops." + value)
		}
	}
	m.message = "Tab chooses controls · Enter continues · Esc cancels"
	return m.wizardMove(1)
}

func (m *model) wizardAction(action string) tea.Cmd {
	switch action {
	case "wizard-next":
		return m.wizardNext()
	case "wizard-back":
		if m.wizard.step > 0 {
			return m.wizardMove(-1)
		}
	case "wizard-cancel":
		m.clearWizard()
		m.navigate(home)
	}
	return nil
}

func (m *model) wizardKey(msg tea.KeyPressMsg) tea.Cmd {
	w := &m.wizard
	summary := w.step == len(w.fields)
	switch msg.String() {
	case "esc":
		return m.wizardAction("wizard-cancel")
	case "tab", "shift+tab":
		if !summary {
			w.fields[w.step].Blur()
		}
		delta, count := 1, 4
		if msg.String() == "shift+tab" {
			delta = -1
		}
		if summary {
			count = 3
		}
		w.focus = (w.focus + delta + count) % count
		if !summary && w.focus == 0 {
			return w.fields[w.step].Focus()
		}
		return nil
	case "enter":
		index := w.focus
		if !summary && index > 0 {
			index--
		}
		return m.wizardAction([]string{"wizard-next", "wizard-back", "wizard-cancel"}[index])
	}
	if !summary && w.focus == 0 {
		var cmd tea.Cmd
		w.fields[w.step], cmd = w.fields[w.step].Update(msg)
		return cmd
	}
	return nil
}

type installationResult struct {
	initialized bool
	preflight   deployment.PreflightReport
	review      deployment.Review
	err         error
}

func (m *model) prepareInstallation(input json.RawMessage) tea.Cmd {
	b := m.installer()
	m.busy, m.mutating = true, len(input) > 0
	m.message = "Preparing configuration and checking readiness…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
		defer cancel()
		var r installationResult
		if len(input) > 0 {
			_, r.err = b.Initialize(ctx, input)
			if r.err != nil {
				return r
			}
			r.initialized = true
		}
		r.preflight, r.err = b.Preflight(ctx)
		if r.err == nil && r.preflight.Status == "candidate" {
			r.review, r.err = m.backend.Review(ctx)
		}
		return r
	}
}

func (m *model) installationContent() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Installation target\n%s\n\n", clean(m.installer().InstallationTarget()))
	b.WriteString("Configuration is prepared. No deployment runs until you review and confirm Apply.\n\n")
	for _, check := range m.installPreflight.Checks {
		mark := "ok"
		if !check.OK {
			mark = "blocked"
		}
		fmt.Fprintf(&b, "[%s] %s\n", mark, clean(check.ID))
		if check.Reason != "" {
			fmt.Fprintf(&b, "  %s\n", clean(check.Reason))
		}
	}
	b.WriteString("\nStop and isolate legacy resources manually. Stopped containers still count as conflicts. This wizard never stops or removes other applications.\n\nCorrect conflicts, then choose Check readiness. An existing draft reopens without entering its secrets again.\n")
	return b.String()
}

type operationProgress struct{ status deployment.OperationStatus }

func (m *model) pollOperation() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		op, _ := m.backend.Operation(ctx)
		return operationProgress{op}
	})
}

func (m *model) navigationScreens() []screen {
	pages := []screen{status, plan, operation}
	if m.supports("configure", "apply") {
		pages = append(pages, password)
	}
	if m.available("install") {
		pages = append(pages, installationForm)
	}
	if m.available("access") {
		pages = append(pages, access)
	}
	return pages
}
func (m *model) navigationLabels() []string {
	labels := []string{"Status", "Plan", "Operation"}
	if m.supports("configure", "apply") {
		labels = append(labels, "Panel password")
	}
	if m.available("install") {
		label := "New installation"
		if m.state.report.Managed {
			label = "Continue setup"
		}
		labels = append(labels, label)
	}
	if m.available("access") {
		labels = append(labels, "Access")
	}
	return labels
}
func (m *model) openNavigation(index int) tea.Cmd {
	pages := m.navigationScreens()
	if index < 0 || index >= len(pages) {
		return nil
	}
	if pages[index] == installationForm {
		return m.dispatch("install")
	}
	if pages[index] == access {
		return m.dispatch("access")
	}
	m.navigate(pages[index])
	return nil
}

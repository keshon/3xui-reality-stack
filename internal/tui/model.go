// Package tui is a fullscreen client of the existing controller API.
// It owns navigation and input, not deployment rules or remote shell commands.
package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type Backend interface {
	control.Reader
	control.Operator
	control.Configurator
	Methods() []string
}

type screen int

const (
	home screen = iota
	status
	plan
	operation
	password
	review
	confirmation
	help
	installationForm
	installationChecks
	access
)

type snapshot struct {
	report    deployment.Report
	plan      deployment.Plan
	runtime   deployment.RuntimeReport
	operation deployment.OperationStatus
	err       error
}

type actionResult struct {
	action    string
	review    deployment.Review
	operation deployment.OperationStatus
	err       error
}

type model struct {
	ctx                             context.Context
	backend                         Backend
	title                           string
	screen                          screen
	selected, focus, width, height  int
	fields                          [2]textinput.Model
	viewport                        viewport.Model
	wizard                          installationWizard
	installPreflight                deployment.PreflightReport
	accessInfo                      *deployment.AccessReport
	showSecrets, accessAfterRefresh bool
	state                           snapshot
	review                          *deployment.Review
	confirmAction, confirmID        string
	busy, mutating                  bool
	message                         string
	menuOpen                        bool
	menu, menuItem                  int
	modalBase, helpBase             screen
	navFocused, confirmBack         bool
}

func newModel(ctx context.Context, backend Backend, title string) *model {
	m := &model{ctx: ctx, backend: backend, title: title, width: 80, height: 24, viewport: viewport.New(), navFocused: true}
	m.viewport.SoftWrap = true
	for i := range m.fields {
		m.fields[i] = textinput.New()
		m.fields[i].EchoMode = textinput.EchoPassword
		m.fields[i].CharLimit = 4096
	}
	m.resize(80, 24)
	return m
}

func (m *model) supports(methods ...string) bool {
	available := m.backend.Methods()
	for _, method := range methods {
		if !slices.Contains(available, method) {
			return false
		}
	}
	return true
}

func (m *model) resize(width, height int) {
	m.width, m.height = width, height
	paneWidth := width
	if width >= 76 {
		paneWidth -= 23
	}
	m.viewport.SetWidth(max(1, paneWidth-4))
	m.viewport.SetHeight(max(1, height-7))
	for i := range m.wizard.fields {
		m.wizard.fields[i].SetWidth(max(1, min(width-16, 48)))
	}
	for i := range m.fields {
		m.fields[i].SetWidth(max(1, min(width-12, 52)))
	}
}

func (m *model) setContent(content string) {
	m.viewport.SetContent(ansi.Wrap(content, m.viewport.Width(), ""))
}

func (m *model) clearPassword() {
	for i := range m.fields {
		m.fields[i].Reset()
		m.fields[i].Blur()
	}
	m.focus = 0
}

func (m *model) navigate(next screen) {
	if next != access {
		m.accessInfo, m.showSecrets = nil, false
	}
	wasModal := m.screen == password || m.screen == confirmation
	openingModal := next == password || next == confirmation
	if m.screen == password {
		m.clearPassword()
	}
	if openingModal {
		m.modalBase = m.screen
		m.confirmBack = false
	}
	m.screen = next
	if m.message != "Request did not complete. Inspect Operation before retrying." && m.message != "Review expired. Create a new review." {
		m.message = ""
	}
	m.navFocused = next == home
	for index, page := range m.navigationScreens() {
		if page == next {
			m.selected = index
		}
	}
	m.menuOpen = false
	if !openingModal && !(wasModal && next == m.modalBase) {
		m.viewport.GotoTop()
	}
	if next == password {
		m.fields[0].Focus()
	}
}

func (m *model) Init() tea.Cmd { return m.refresh() }

func (m *model) refresh() tea.Cmd {
	m.busy = true
	m.review = nil
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
		defer cancel()
		var s snapshot
		s.operation, s.err = m.backend.Operation(ctx)
		if s.err != nil {
			return s
		}
		if s.report, s.err = m.backend.Inspect(ctx); s.err != nil {
			return s
		}
		if s.plan, s.err = m.backend.Plan(ctx); s.err != nil {
			return s
		}
		if m.installer() != nil && !s.report.Baseline {
			return s
		}
		if s.runtime, s.err = m.backend.Runtime(ctx); s.err != nil {
			return s
		}
		s.operation, s.err = m.backend.Operation(ctx)
		return s
	}
}

func (m *model) perform(action, id, secret string) tea.Cmd {
	m.busy = true
	m.mutating = action == "apply" || action == "resume" || action == "cancel" || action == "configure"
	m.message = "Working…"
	work := func() tea.Msg {
		budget := 2 * time.Minute
		if m.installer() != nil && (action == "apply" || action == "resume") {
			budget = 20 * time.Minute
		}
		ctx, cancel := context.WithTimeout(m.ctx, budget)
		defer cancel()
		r := actionResult{action: action}
		switch action {
		case "configure":
			_, r.err = m.backend.Configure(ctx, map[string]string{"PANEL_PASS": secret})
		case "review":
			r.review, r.err = m.backend.Review(ctx)
		case "apply":
			r.operation, r.err = m.backend.ApplyReview(ctx, id, true)
		case "resume":
			r.operation, r.err = m.backend.ResumeOperation(ctx, id)
		case "cancel":
			r.operation, r.err = m.backend.CancelPreparation(ctx, id)
		}
		return r
	}
	if m.installer() != nil && (action == "apply" || action == "resume") {
		return tea.Batch(work, m.pollOperation())
	}
	return work
}

func Run(ctx context.Context, backend Backend, input io.Reader, output io.Writer, title string) error {
	in, inOK := input.(*os.File)
	out, outOK := output.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return errors.New("TUI requires an interactive terminal for input and output")
	}
	uiCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(uiCtx, backend, title)
	inputOptions, restoreInput, err := consoleInput(uiCtx, in, out)
	if err != nil {
		return errors.New("could not initialize terminal input")
	}
	defer restoreInput()
	options := []tea.ProgramOption{tea.WithContext(uiCtx), tea.WithInput(in), tea.WithOutput(out)}
	options = append(options, inputOptions...)
	_, err = tea.NewProgram(m, options...).Run()
	m.clearPassword()
	m.clearWizard()
	if m.mutating {
		return errors.New("TUI closed during a request; inspect operation status before retrying")
	}
	if err != nil {
		return errors.New("terminal session ended; reconnect and inspect operation status")
	}
	return nil
}

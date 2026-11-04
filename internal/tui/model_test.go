package tui

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type fakeBackend struct {
	writable                                                              bool
	configured                                                            string
	configurationCalls, reviewCalls, applyCalls, resumeCalls, cancelCalls int
	p                                                                     deployment.Plan
	op                                                                    deployment.OperationStatus
	err                                                                   error
}

func (b *fakeBackend) Methods() []string {
	methods := []string{"inspect", "plan", "runtime", "operation", "review"}
	if b.writable {
		methods = append(methods, "configure", "apply", "resume", "cancel-preparation")
	}
	return methods
}
func (b *fakeBackend) Inspect(context.Context) (deployment.Report, error) {
	return deployment.Report{Stack: "fixture", Managed: true, Baseline: true}, nil
}
func (b *fakeBackend) Plan(context.Context) (deployment.Plan, error) { return b.p, nil }
func (b *fakeBackend) Runtime(context.Context) (deployment.RuntimeReport, error) {
	return deployment.RuntimeReport{Stack: "fixture", Status: "compatible"}, nil
}
func (b *fakeBackend) Operation(context.Context) (deployment.OperationStatus, error) {
	if b.op.Status == "" {
		return deployment.OperationStatus{Status: "idle"}, nil
	}
	return b.op, nil
}
func (b *fakeBackend) Configure(_ context.Context, changes map[string]string) (deployment.Plan, error) {
	b.configurationCalls++
	b.configured = changes["PANEL_PASS"]
	b.p.Status = "proposed"
	return b.p, b.err
}
func (b *fakeBackend) Review(context.Context) (deployment.Review, error) {
	b.reviewCalls++
	return deployment.Review{ID: strings.Repeat("a", 32), Kind: "xui-password-v1", Plan: b.p, CanApply: b.writable, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, b.err
}
func (b *fakeBackend) ApplyReview(_ context.Context, id string, confirmed bool) (deployment.OperationStatus, error) {
	if id != strings.Repeat("a", 32) || !confirmed {
		return deployment.OperationStatus{}, errors.New("wrong confirmation")
	}
	b.applyCalls++
	b.p.Status = "unchanged"
	b.op = deployment.OperationStatus{ID: strings.Repeat("b", 32), Status: "complete", Completed: 3, Total: 3}
	return b.op, b.err
}
func (b *fakeBackend) ResumeOperation(_ context.Context, id string) (deployment.OperationStatus, error) {
	b.resumeCalls++
	return deployment.OperationStatus{ID: id, Status: "complete"}, b.err
}
func (b *fakeBackend) CancelPreparation(_ context.Context, id string) (deployment.OperationStatus, error) {
	b.cancelCalls++
	return deployment.OperationStatus{ID: id, Status: "cancelled"}, b.err
}

func press(m *model, key string) tea.Cmd {
	code := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"f1": tea.KeyF1, "f2": tea.KeyF2, "f3": tea.KeyF3, "f4": tea.KeyF4, "f5": tea.KeyF5, "f6": tea.KeyF6, "f7": tea.KeyF7, "f8": tea.KeyF8, "f9": tea.KeyF9, "f10": tea.KeyF10}[key]
	if code == 0 {
		code = []rune(key)[0]
	}
	msg := tea.KeyPressMsg{Code: code}
	if len(key) == 1 {
		msg.Text = key
	}
	_, cmd := m.Update(msg)
	return cmd
}

func finish(m *model, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

func readyModel(t *testing.T, writable bool) (*model, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{writable: writable, p: deployment.Plan{Stack: "fixture", Status: "unchanged", ReadOnly: true}}
	m := newModel(t.Context(), b, "Test controller")
	finish(m, m.Init())
	return m, b
}

func TestFullscreenNavigationUsesOnlyReadsByDefault(t *testing.T) {
	m, b := readyModel(t, false)
	if !m.View().AltScreen {
		t.Fatal("TUI did not request fullscreen alternate screen")
	}
	if strings.Contains(m.View().Content, "Change panel password") {
		t.Fatal("read-only controller exposed password changes")
	}
	for _, page := range []screen{status, plan, operation} {
		m.selected = int(page) - 1
		press(m, "enter")
		if m.screen != page {
			t.Fatal("navigation opened the wrong screen")
		}
		press(m, "esc")
		if m.screen != home {
			t.Fatal("Escape did not return home")
		}
	}
	if b.configurationCalls+b.reviewCalls+b.applyCalls+b.resumeCalls+b.cancelCalls != 0 {
		t.Fatal("navigation performed an operation")
	}
}

func TestPasswordRequiresMaskedInputReviewAndSeparateConfirmation(t *testing.T) {
	m, b := readyModel(t, true)
	m.selected = 3
	press(m, "enter")
	if m.screen != password {
		t.Fatal("password screen not reachable")
	}
	press(m, "q")
	if m.fields[0].Value() != "q" || m.screen != password {
		t.Fatal("password input used a global navigation shortcut")
	}
	secret := "private-$HOME-password"
	m.fields[0].SetValue(secret)
	m.fields[1].SetValue(secret)
	if strings.Contains(m.View().Content, secret) {
		t.Fatal("password appeared in terminal output")
	}
	press(m, "enter")
	cmd := press(m, "enter")
	if m.fields[0].Value() != "" || m.fields[1].Value() != "" {
		t.Fatal("submitted password stayed in the form")
	}
	finish(m, cmd)
	if b.configured != secret || b.configurationCalls != 1 || b.applyCalls != 0 || m.screen != plan {
		t.Fatal("editing a password bypassed review")
	}
	finish(m, press(m, "v"))
	if m.screen != review || b.reviewCalls != 1 || b.applyCalls != 0 {
		t.Fatal("review executed a change")
	}
	press(m, "a")
	if m.screen != confirmation || b.applyCalls != 0 {
		t.Fatal("Apply did not require confirmation")
	}
	press(m, "esc")
	if b.applyCalls != 0 || m.screen != review {
		t.Fatal("canceling confirmation performed a write")
	}
	press(m, "a")
	cmd = press(m, "enter")
	press(m, "enter") // Busy state must not submit twice.
	finish(m, cmd)
	if b.applyCalls != 1 || m.screen != operation || m.state.operation.Status != "complete" {
		t.Fatal("reviewed operation did not complete exactly once")
	}
	if strings.Contains(m.View().Content, secret) {
		t.Fatal("completed screen retained a password")
	}
}

func TestCancelPasswordAndExpiredReviewCannotWrite(t *testing.T) {
	m, b := readyModel(t, true)
	m.selected = 3
	press(m, "enter")
	m.fields[0].SetValue("private")
	press(m, "esc")
	if m.fields[0].Value() != "" || b.configurationCalls != 0 {
		t.Fatal("canceling a form retained or submitted its password")
	}
	b.p.Status = "proposed"
	m.state.plan = b.p
	m.navigate(plan)
	finish(m, press(m, "v"))
	m.review.ExpiresAt = time.Now().Add(-time.Second)
	press(m, "a")
	press(m, "enter")
	if b.applyCalls != 0 || m.screen != plan {
		t.Fatal("expired review reached apply")
	}
}

func TestRecoveryRequiresExplicitConfirmationAndSavedOperationID(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		m, b := readyModel(t, true)
		b.op = deployment.OperationStatus{ID: strings.Repeat("c", 32), Status: "preparing"}
		m.state.operation = b.op
		m.navigate(operation)
		key := "s"
		if cancel {
			key = "c"
		}
		press(m, key)
		if m.confirmID != b.op.ID || b.resumeCalls+b.cancelCalls != 0 {
			t.Fatal("recovery skipped confirmation or changed the target")
		}
		finish(m, press(m, "enter"))
		if b.resumeCalls+b.cancelCalls != 1 {
			t.Fatal("recovery ran more than once")
		}
	}
}

func TestScreensFitTerminalAndIgnoreInvisibleActions(t *testing.T) {
	m, b := readyModel(t, true)
	for _, size := range [][2]int{{40, 18}, {80, 24}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, page := range []screen{home, status, plan, operation, password} {
			m.navigate(page)
			m.viewport.SetContent(m.content())
			frame := ansi.Strip(m.View().Content)
			if len(strings.Split(frame, "\n")) > size[1] {
				t.Fatal("screen exceeded terminal height")
			}
			for _, line := range strings.Split(frame, "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatal("screen exceeded terminal width")
				}
			}
		}
	}
	m.navigate(confirmation)
	m.confirmAction = "resume"
	m.confirmID = strings.Repeat("c", 32)
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	press(m, "enter")
	if b.resumeCalls != 0 {
		t.Fatal("invisible confirmation performed an action")
	}
}

func TestControllerErrorsDoNotExposePrivateInputOrRetryWrites(t *testing.T) {
	m, b := readyModel(t, true)
	b.err = errors.New("private-server-password")
	finish(m, m.perform("configure", "", "private-user-password"))
	if b.configurationCalls != 1 || strings.Contains(m.View().Content, "private-") {
		t.Fatal("failed request leaked data or repeated a write")
	}
}

func TestSameTUIReadsThroughStdioClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	left, right := net.Pipe()
	b := &fakeBackend{p: deployment.Plan{Stack: "fixture", Status: "unchanged", ReadOnly: true}}
	done := make(chan struct{})
	go func() { defer close(done); defer right.Close(); _ = control.Serve(ctx, right, right, b) }()
	client, err := control.Connect(ctx, left)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(ctx, client, "SSH controller")
	finish(m, m.Init())
	if m.state.runtime.Status != "compatible" || m.state.report.Stack != "fixture" || m.supports("apply") {
		t.Fatal("remote TUI bypassed negotiated controller capabilities")
	}
	client.Close()
	<-done
}

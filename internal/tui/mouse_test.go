package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Locate the visible label rather than trusting hit-test coordinates.
func clickLabel(t *testing.T, m *model, label string) tea.Cmd {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if at := strings.Index(line, label); at >= 0 {
			_, cmd := m.Update(tea.MouseClickMsg{X: ansi.StringWidth(line[:at]) + 1, Y: y, Button: tea.MouseLeft})
			return cmd
		}
	}
	t.Fatalf("visible control %q missing", label)
	return nil
}

func TestMouseNavigationMenusFooterAndPasswordRoundTrip(t *testing.T) {
	m, b := readyModel(t, true)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse reporting disabled")
	}
	clickLabel(t, m, "Actions")
	clickLabel(t, m, "Panel password    F6")
	if m.screen != password {
		t.Fatal("menu click did not open password dialog")
	}
	m.Update(tea.PasteMsg{Content: "pasted-q-password"})
	if m.fields[0].Value() != "pasted-q-password" || strings.Contains(m.View().Content, "pasted-q-password") {
		t.Fatal("paste missing or unmasked")
	}
	_, hits := m.compose()
	for _, hit := range hits {
		if hit.action == "field" && hit.index == 1 {
			m.Update(tea.MouseClickMsg{X: hit.x + 1, Y: hit.y, Button: tea.MouseLeft})
		}
	}
	m.Update(tea.PasteMsg{Content: "pasted-q-password"})
	finish(m, clickLabel(t, m, "[ Save desired ]"))
	if m.screen != plan || b.configurationCalls != 1 || b.applyCalls != 0 {
		t.Fatal("mouse form skipped desired/review boundary")
	}
	finish(m, clickLabel(t, m, "F7 Review plan"))
	clickLabel(t, m, "a Apply")
	if m.screen != confirmation || b.applyCalls != 0 {
		t.Fatal("mouse skipped confirmation")
	}
	finish(m, clickLabel(t, m, "[ Apply ]"))
	if b.applyCalls != 1 || m.state.operation.Status != "complete" {
		t.Fatal("mouse confirmed operation failed")
	}
	clickLabel(t, m, "Help")
	if m.screen != help {
		t.Fatal("footer click did not open Help")
	}
	clickLabel(t, m, "[ Close ]")
	if m.screen != operation {
		t.Fatal("help close did not restore original screen")
	}
}

func TestMouseScrollIsBoundToReportAndBlockedByModal(t *testing.T) {
	m, _ := readyModel(t, true)
	m.state.plan.Reasons = []string{strings.Repeat("report line\n", 50)}
	m.navigate(plan)
	m.viewport.SetContent(m.content())
	wheel := tea.MouseWheelMsg{X: 30, Y: 7, Button: tea.MouseWheelDown}
	m.Update(wheel)
	if m.viewport.YOffset() == 0 {
		t.Fatal("wheel did not scroll report")
	}
	offset := m.viewport.YOffset()
	m.Update(tea.MouseWheelMsg{X: 3, Y: 7, Button: tea.MouseWheelDown})
	if m.viewport.YOffset() != offset {
		t.Fatal("wheel over navigation scrolled report")
	}
	press(m, "f6")
	m.Update(wheel)
	if m.viewport.YOffset() != offset {
		t.Fatal("wheel leaked through password modal")
	}
	clickLabel(t, m, "[ Cancel ]")
	if m.screen != plan || m.viewport.YOffset() != offset {
		t.Fatal("mouse cancel lost report position")
	}
}

func TestSuccessfulRefreshClearsProgressAndExpiryDisablesApply(t *testing.T) {
	m, _ := readyModel(t, true)
	finish(m, press(m, "f5"))
	if m.message != "" {
		t.Fatal("refresh progress persisted after completion")
	}
	m.state.plan.Status = "proposed"
	finish(m, press(m, "f7"))
	m.review.ExpiresAt = time.Now().Add(-time.Second)
	if m.available("apply") {
		t.Fatal("expired review remained actionable")
	}
}

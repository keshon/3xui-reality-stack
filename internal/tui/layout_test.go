package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestMenuAndFunctionKeysUseCapabilitiesAndKeepModalFocus(t *testing.T) {
	m, b := readyModel(t, false)
	press(m, "f6")
	if m.screen == password {
		t.Fatal("read-only shortcut opened password form")
	}
	press(m, "f9")
	press(m, "down") // Status in Workspace.
	press(m, "enter")
	if m.menuOpen || m.screen != status {
		t.Fatal("menu did not open Status")
	}
	press(m, "tab")
	press(m, "down")
	press(m, "enter")
	if m.screen != plan || m.navFocused {
		t.Fatal("pane focus did not navigate to Plan")
	}
	press(m, "f1")
	press(m, "f6")
	if m.screen != help {
		t.Fatal("global shortcut escaped the help modal")
	}
	press(m, "esc")
	if m.screen != plan {
		t.Fatal("help did not restore its original screen")
	}
	if b.configurationCalls+b.applyCalls+b.reviewCalls != 0 {
		t.Fatal("navigation performed a write")
	}

	m, b = readyModel(t, true)
	press(m, "f6")
	m.fields[0].SetValue("dummy")
	m.fields[1].SetValue("dummy")
	press(m, "f9")
	if m.menuOpen || m.screen != password {
		t.Fatal("menu stole password input focus")
	}
	press(m, "tab")
	press(m, "tab")
	press(m, "tab") // Cancel button.
	press(m, "enter")
	if m.fields[0].Value() != "" || b.configurationCalls != 0 {
		t.Fatal("Cancel submitted or retained a password")
	}
	b.p.Status, m.state.plan.Status = "proposed", "proposed"
	finish(m, press(m, "f7"))
	press(m, "a")
	press(m, "tab") // Back button.
	press(m, "enter")
	if m.screen != review || b.applyCalls != 0 {
		t.Fatal("modal Back performed an apply")
	}
}

func TestFramedScreensMenusAndDialogsFitEveryTerminalSize(t *testing.T) {
	m, _ := readyModel(t, true)
	m.review = &deployment.Review{ID: strings.Repeat("a", 32), CanApply: true}
	m.confirmAction, m.confirmID = "cancel", strings.Repeat("c", 32)
	for _, size := range [][2]int{{40, 18}, {76, 18}, {80, 24}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		check := func() string {
			t.Helper()
			frame := ansi.Strip(m.View().Content)
			lines := strings.Split(frame, "\n")
			if len(lines) != size[1] {
				t.Fatalf("screen height %d, want %d", len(lines), size[1])
			}
			for _, line := range lines {
				if ansi.StringWidth(line) != size[0] {
					t.Fatalf("screen width %d, want %d: %q", ansi.StringWidth(line), size[0], line)
				}
			}
			return frame
		}
		for _, page := range []screen{home, status, plan, operation, review, password, confirmation, help} {
			m.modalBase, m.helpBase = plan, plan
			m.navigate(page)
			m.viewport.SetContent(m.content())
			frame := check()
			if page == password && !strings.Contains(frame, "[ Save desired ]") {
				t.Fatal("password actions were clipped")
			}
			if page == confirmation && !strings.Contains(frame, "[ Back ]") {
				t.Fatalf("confirmation actions were clipped at %v:\n%s", size, frame)
			}
			if page == help && !strings.Contains(frame, "Close ]") {
				t.Fatal("help close hint was clipped")
			}
		}
		m.navigate(home)
		m.menuOpen = true
		for i := 0; i < 3; i++ {
			m.menu = i
			check()
		}
		m.menuOpen = false
	}
}

func TestDialogsRestoreReportAndScrollPosition(t *testing.T) {
	m, _ := readyModel(t, true)
	m.state.plan.Reasons = []string{strings.Repeat("report line\n", 40)}
	m.navigate(plan)
	m.viewport.SetContent(m.content())
	m.viewport.SetYOffset(5)
	press(m, "f6")
	press(m, "esc")
	if m.screen != plan || m.viewport.YOffset() != 5 {
		t.Fatal("canceling password dialog lost the original report position")
	}
	press(m, "f1")
	press(m, "enter")
	if m.screen != plan || m.viewport.YOffset() != 5 {
		t.Fatal("help lost the original report position")
	}
	press(m, "f9")
	press(m, "f3")
	if m.screen != status || m.menuOpen {
		t.Fatal("function keys did not work while the main menu was open")
	}
}

func TestAccessChromeHasExactCellsAndVisibleMouseTargets(t *testing.T) {
	for _, size := range [][2]int{{40, 18}, {41, 19}, {60, 24}, {75, 24}, {76, 18}, {80, 24}, {100, 30}, {120, 40}, {160, 48}} {
		b := &accessBackend{fakeBackend: fakeBackend{p: deployment.Plan{Stack: "fixture", Status: "unchanged", ReadOnly: true}}}
		m := newModel(t.Context(), b, "SSH controller · /opt/example-stack")
		finish(m, m.Init())
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		finish(m, m.dispatch("access"))
		view, hits := m.compose()
		rows := strings.Split(ansi.Strip(view.Content), "\n")
		if len(rows) != size[1] {
			t.Fatalf("frame height at %v: %d", size, len(rows))
		}
		for y, row := range rows {
			if ansi.StringWidth(row) != size[0] {
				t.Fatalf("frame width at %v row %d: %d", size, y, ansi.StringWidth(row))
			}
		}
		if !strings.HasPrefix(rows[0], " Workspace ") || strings.Contains(rows[0], "STACK") || !strings.HasPrefix(rows[1], "┌") {
			t.Fatalf("extra branding/header space at %v", size)
		}
		if !strings.Contains(rows[len(rows)-1], "SSH controller") || strings.Contains(view.Content, "10Quit") {
			t.Fatalf("target footer or removed function bar is wrong at %v", size)
		}
		for _, hit := range hits {
			if hit.x < 0 || hit.y < 0 || hit.x+hit.width > size[0] || hit.y+hit.height > size[1] {
				t.Fatalf("mouse region outside frame at %v: %+v", size, hit)
			}
			text := ansi.Cut(rows[hit.y], hit.x, hit.x+hit.width)
			switch hit.action {
			case "menu":
				if strings.TrimSpace(text) != topMenus[hit.index] {
					t.Fatalf("menu region misses visible label at %v: %q", size, text)
				}
			case "navigate":
				if !strings.Contains(text, m.navigationLabels()[hit.index]) {
					t.Fatalf("navigation region misses visible label at %v: %q", size, text)
				}
			case "toggle-secrets":
				if !strings.Contains(text, "Show password") {
					t.Fatalf("context region misses visible control at %v: %q", size, text)
				}
			}
		}
	}
}

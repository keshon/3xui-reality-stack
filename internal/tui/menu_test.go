package tui

import "testing"

func TestKeyboardMenusSkipUnavailableActions(t *testing.T) {
	m, _ := readyModel(t, false)
	m.state.plan.Status = "proposed"
	m.menu = 1
	m.dispatch("menu")
	if m.menuItems()[m.menuItem].action != "review" {
		t.Fatal("opening menu selected an unavailable action")
	}
	m.menuKey("down")
	if m.menuItems()[m.menuItem].action != "review" {
		t.Fatal("down failed to skip unavailable actions")
	}
	m.menuKey("up")
	if m.menuItems()[m.menuItem].action != "review" {
		t.Fatal("up failed to skip unavailable actions")
	}
	m.menu = 0
	m.menuItem = 3 // Operation; Access is unavailable in this backend.
	m.menuKey("down")
	if m.menuItems()[m.menuItem].action != "home" {
		t.Fatal("menu did not skip unavailable Access when wrapping")
	}
	m.menu, m.menuItem = 1, 0
	m.state.plan.Status = "unchanged"
	m.menuKey("down") // Every item is disabled: must terminate and stay in range.
	if m.menuItem < 0 || m.menuItem >= len(m.menuItems()) {
		t.Fatal("empty enabled menu lost its selection")
	}
}

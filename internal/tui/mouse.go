package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type hitRegion struct {
	x, y, width, height int
	action              string
	index               int
}

func (h hitRegion) contains(x, y int) bool {
	return x >= h.x && x < h.x+h.width && y >= h.y && y < h.y+h.height
}

func moveHits(hits []hitRegion, x, y int) []hitRegion {
	for i := range hits {
		hits[i].x += x
		hits[i].y += y
	}
	return hits
}

type footerCell struct{ x, width, index int }

func (m *model) footerCells() []footerCell {
	var cells []footerCell
	x := 0
	for i := 0; i < 10; i++ {
		if m.width < 76 && i != 0 && i != 4 && i != 8 && i != 9 {
			continue
		}
		width := m.width / 10
		if m.width < 76 {
			width = m.width / 4
		}
		if i == 9 {
			width = m.width - x
		}
		cells = append(cells, footerCell{x, width, i})
		x += width
	}
	return cells
}

var footerActions = []string{"help", "home", "status", "plan", "refresh", "password", "review", "operation", "menu", "quit"}

func (m *model) menuX() int { return min([]int{7, 18, 27}[m.menu], m.width-30) }

func (m *model) topAndFooterRegions() []hitRegion {
	hits := []hitRegion{{7, 0, 11, 1, "menu", 0}, {18, 0, 9, 1, "menu", 1}, {27, 0, 9, 1, "menu", 2}}
	for _, cell := range m.footerCells() {
		action := footerActions[cell.index]
		if action == "menu" {
			action = "open-menu"
		}
		hits = append(hits, hitRegion{cell.x, m.height - 1, cell.width, 1, action, 0})
	}
	return hits
}

func (m *model) chromeRegions() []hitRegion {
	hits := m.topAndFooterRegions()
	count := 3
	if m.supports("configure", "apply") {
		count++
	}
	if m.width >= 76 || m.screen == home {
		width := m.width - 2
		if m.width >= 76 {
			width = 21
		}
		for i := 0; i < count; i++ {
			hits = append(hits, hitRegion{1, 3 + i, width, 1, "navigate", i})
		}
	}
	x := 0
	if m.width >= 76 {
		x = 23
	}
	if m.screen != home {
		hits = append(hits, hitRegion{x + 1, 3, m.width - x - 2, m.height - 8, "report", 0})
	}
	x += 2
	end := m.width - 1 - ansi.StringWidth(m.scrollIndicator())
	for _, item := range m.contextActions() {
		width := min(ansi.StringWidth(item.label), end-x)
		if width > 0 {
			hits = append(hits, hitRegion{x, m.height - 4, width, 1, item.action, 0})
		}
		x += ansi.StringWidth(item.label) + 3
	}
	return hits
}

func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.(type) {
	case tea.MouseClickMsg, tea.MouseWheelMsg:
	default:
		return nil
	}
	if m.width < 40 || m.height < 18 || m.busy {
		return nil
	}
	mouse := msg.Mouse()
	_, hits := m.compose()
	var hit *hitRegion
	for i := len(hits) - 1; i >= 0; i-- {
		if hits[i].contains(mouse.X, mouse.Y) {
			hit = &hits[i]
			break
		}
	}
	if _, wheel := msg.(tea.MouseWheelMsg); wheel {
		if hit != nil && hit.action == "report" && !m.menuOpen {
			m.navFocused = false
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return cmd
		}
		return nil
	}
	if _, click := msg.(tea.MouseClickMsg); !click || mouse.Button != tea.MouseLeft {
		return nil
	}
	if hit == nil {
		m.menuOpen = false
		return nil
	}
	switch hit.action {
	case "open-menu":
		if m.menuOpen {
			m.menuOpen = false
			return nil
		}
		return m.dispatch("menu")
	case "menu":
		if m.menuOpen && m.menu == hit.index {
			m.menuOpen = false
			return nil
		}
		m.menu, m.menuItem, m.menuOpen = hit.index, 0, true
	case "menu-item":
		m.menuItem = hit.index
		return m.dispatch(m.menuItems()[hit.index].action)
	case "navigate":
		m.navigate([]screen{status, plan, operation, password}[hit.index])
	case "report":
		m.navFocused = false
	case "field":
		if m.focus < 2 {
			m.fields[m.focus].Blur()
		}
		m.focus = hit.index
		return m.fields[m.focus].Focus()
	case "save":
		m.focus = 2
		return m.passwordKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "confirm":
		m.confirmBack = false
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return cmd
	case "close":
		if m.screen == help {
			m.screen = m.helpBase
		} else {
			m.navigate(m.modalBase)
		}
	default:
		return m.dispatch(hit.action)
	}
	return nil
}

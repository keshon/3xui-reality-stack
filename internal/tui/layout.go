package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	accent   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8ED4DE")).Bold(true)
	muted    = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EADBC"))
	border   = lipgloss.NewStyle().Foreground(lipgloss.Color("#607B91"))
	selected = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#355A73")).Bold(true)
	bar      = lipgloss.NewStyle().Foreground(lipgloss.Color("#D9E2EB")).Background(lipgloss.Color("#263B4C"))
)

var screenNames = []string{"Overview", "Status", "Plan", "Operation", "Panel password", "Review", "Confirm action", "Help"}

// cell and panel always return fixed cell dimensions, including ANSI and wide
// characters. Layout has one owner; individual screens only supply content.
func cell(s string, width int) string {
	s = ansi.Truncate(s, max(0, width), "")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

// Nested styles reset terminal colors. Restore the enclosing surface after a
// reset so selection, borders and input controls do not leave unpainted cells.
// Deriving the sequences through Lip Gloss preserves NO_COLOR/profile handling.
func paint(s, foreground, background string) string {
	prefix := func(style lipgloss.Style) string {
		sample := style.Render("x")
		return sample[:strings.Index(sample, "x")]
	}
	fg := prefix(lipgloss.NewStyle().Foreground(lipgloss.Color(foreground)))
	bg := prefix(lipgloss.NewStyle().Background(lipgloss.Color(background)))
	if fg == "" && bg == "" {
		return s
	}
	reset := fg + bg
	restore := strings.NewReplacer("\x1b[m", "\x1b[m"+reset, "\x1b[0m", "\x1b[0m"+reset, "\x1b[39m", fg, "\x1b[49m", bg)
	return reset + restore.Replace(s) + "\x1b[m"
}

func panel(title, body string, width, height int) string {
	inner := max(0, width-2)
	title = ansi.Truncate(" "+title+" ", max(0, inner-2), "…")
	top := border.Render("┌─") + accent.Render(title) + border.Render(strings.Repeat("─", max(0, inner-1-ansi.StringWidth(title)))+"┐")
	rows := []string{paint(cell(top, width), "#D9E2EB", "#17212C")}
	lines := strings.Split(body, "\n")
	for i := 0; i < max(0, height-2); i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		rows = append(rows, paint(border.Render("│")+cell(line, inner)+border.Render("│"), "#D9E2EB", "#17212C"))
	}
	rows = append(rows, paint(border.Render("└"+strings.Repeat("─", inner)+"┘"), "#D9E2EB", "#17212C"))
	return strings.Join(rows, "\n")
}

func inset(body string) string {
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = " " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func (m *model) navigation() string {
	var rows []string
	labels := []string{"Status", "Plan", "Operation"}
	if m.supports("configure", "apply") {
		labels = append(labels, "Panel password")
	}
	current := map[screen]int{status: 0, plan: 1, operation: 2, password: 3}
	for i, label := range labels {
		line := fmt.Sprintf(" %d  %s", i+1, label)
		active := m.navFocused && m.selected == i
		if index, ok := current[m.screen]; ok && !m.navFocused {
			active = index == i
		}
		if active {
			if m.navFocused {
				line = selected.Render(">" + cell(line, 21)[1:])
			} else {
				line = muted.Render("·" + line[1:])
			}
		}
		rows = append(rows, line)
	}
	rows = append(rows, "", muted.Render(" Enter  Open"), muted.Render(" Tab    Switch pane"), muted.Render(" F2     Overview"), "", " Session", "  "+m.accessMode())
	return strings.Join(rows, "\n")
}

func (m *model) accessMode() string {
	if m.supports("apply") {
		return "Password edits"
	}
	return "Read only"
}

func (m *model) overview() string {
	value := func(s string) string {
		if s == "" {
			return "waiting"
		}
		return clean(s)
	}
	return fmt.Sprintf("\n%s\n\n  Stack       %s\n  Runtime     %s\n  Plan        %s\n  Operation   %s\n\n%s\n%s",
		accent.Render(" Existing installation"), value(m.state.report.Stack), value(m.state.runtime.Status), value(m.state.plan.Status), value(m.state.operation.Status),
		muted.Render(" Choose a task from the navigation panel."), muted.Render(" F9 opens the menu. F1 shows keyboard help."))
}

func (m *model) workspace(page screen) string {
	width, height := m.width, m.height-4
	copy := *m
	copy.screen = page
	body := m.viewport.View()
	if page == home {
		body = m.overview()
		if width < 76 {
			body = copy.content()
		}
	}
	rightWidth := width
	if width >= 76 {
		rightWidth -= 23
	}
	lines := strings.Split(inset(body), "\n")
	rows := make([]string, height-4)
	for i := range rows {
		if i < len(lines) {
			rows[i] = cell(lines[i], rightWidth-2)
		} else {
			rows[i] = cell("", rightWidth-2)
		}
	}
	hint := cell(" "+copy.contextHint(), rightWidth-2)
	if position := copy.scrollIndicator(); position != "" {
		hint = cell(" "+copy.contextHint(), rightWidth-2-ansi.StringWidth(position)) + muted.Render(position)
	}
	rows = append(rows, border.Render(strings.Repeat("─", rightWidth-2)), hint)
	title := screenNames[page]
	if !m.navFocused {
		title += " *"
	}
	right := panel(title, strings.Join(rows, "\n"), rightWidth, height)
	if width < 76 {
		return right
	}
	navTitle := "Navigation"
	if m.navFocused {
		navTitle += " *"
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panel(navTitle, copy.navigation(), 23, height), right)
}

func (m *model) scrollIndicator() string {
	if m.screen != home && m.viewport.TotalLineCount() > m.viewport.Height() {
		return fmt.Sprintf(" %3.0f%% ", m.viewport.ScrollPercent()*100)
	}
	return ""
}

func (m *model) menuBar() string {
	line := accent.Render(" STACK ")
	for i, label := range []string{"Workspace", "Actions", "Session"} {
		item := " " + label + " "
		if m.menuOpen && m.menu == i {
			item = selected.Render(item)
		}
		line += item
	}
	return paint(bar.Render(cell(line, m.width)), "#D9E2EB", "#263B4C")
}

func overlay(background, foreground string, x, y int) string {
	rows := strings.Split(background, "\n")
	for i, line := range strings.Split(foreground, "\n") {
		if y+i < 0 || y+i >= len(rows) {
			continue
		}
		row := rows[y+i]
		rows[y+i] = ansi.Cut(row, 0, x) + line + ansi.Cut(row, x+ansi.StringWidth(line), ansi.StringWidth(row))
	}
	return strings.Join(rows, "\n")
}

func (m *model) functionBar() string {
	labels := []string{"Help", "Home", "Status", "Plan", "Reload", "Passwd", "Review", "Operat", "Menu", "Quit"}
	var cells []string
	for _, item := range m.footerCells() {
		i, width := item.index, item.width
		label := labels[i]
		action := footerActions[i]
		style := bar
		foreground := "#D9E2EB"
		if !m.available(action) || m.busy || m.screen == password || m.screen == confirmation || m.screen == help {
			foreground = "#738394"
			style = style.Foreground(lipgloss.Color(foreground))
		}
		number := fmt.Sprintf("%d", i+1)
		label = cell(label, width-len(number)-1)
		cells = append(cells, paint(style.Render(accent.Render(number)+label+" "), foreground, "#263B4C"))
	}
	return strings.Join(cells, "")
}

func (m *model) render() tea.View {
	v, _ := m.compose()
	return v
}

func (m *model) compose() (tea.View, []hitRegion) {
	if m.width < 40 || m.height < 18 {
		v := tea.NewView("Resize to at least 40 × 18. q quit")
		v.AltScreen = true
		return v, nil
	}
	regions := m.chromeRegions()
	page := m.screen
	if page == password || page == confirmation {
		page = m.modalBase
	}
	if page == help {
		page = m.helpBase
	}
	message := m.message
	if message == "" {
		message = "Ready · arrows navigate · Enter opens · Esc returns"
		if m.screen == password {
			message = "Tab moves · Enter saves · Esc cancels"
		}
		if m.screen == confirmation {
			message = "Tab chooses · Enter activates · Esc returns"
		}
		if m.screen == help {
			message = "Enter or Esc closes help"
		}
		if m.menuOpen {
			message = "Arrows choose · Enter selects · Esc closes menu"
		}
	}
	if m.busy {
		message = "Working…"
	}
	mode := " " + m.accessMode() + " "
	connection := cell(" "+clean(m.title), max(0, m.width-ansi.StringWidth(mode))) + accent.Render(mode)
	frame := m.menuBar() + "\n" + paint(bar.Render(connection), "#D9E2EB", "#263B4C") + "\n" + m.workspace(page) + "\n" + muted.Render(cell(" "+clean(message), m.width)) + "\n" + m.functionBar()
	if m.menuOpen {
		items := m.menuItems()
		var rows []string
		for i, item := range items {
			line := cell(" "+item.label, 28)
			if !m.available(item.action) {
				line = muted.Render(line)
			} else if i == m.menuItem {
				line = selected.Render(">" + line[1:])
			}
			rows = append(rows, line)
		}
		x := m.menuX()
		regions = m.topAndFooterRegions()
		for i := range items {
			regions = append(regions, hitRegion{x + 1, 2 + i, 28, 1, "menu-item", i})
		}
		frame = overlay(frame, panel([]string{"Workspace", "Actions", "Session"}[m.menu], strings.Join(rows, "\n"), 30, len(rows)+2), x, 1)
	}
	if m.screen == password || m.screen == confirmation || m.screen == help {
		dialog, hits := m.dialog()
		x, y := (m.width-lipgloss.Width(dialog))/2, (m.height-lipgloss.Height(dialog))/2
		frame = overlay(frame, dialog, x, y)
		regions = moveHits(hits, x, y)
	}
	v := tea.NewView(frame)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor, v.ForegroundColor = lipgloss.Color("#111821"), lipgloss.Color("#D9E2EB")
	return v, regions
}

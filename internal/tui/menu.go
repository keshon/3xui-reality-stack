package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"time"
)

type menuEntry struct{ label, action string }

var functionActions = map[string]string{"f1": "help", "f2": "home", "f3": "status", "f4": "plan", "f5": "refresh", "f6": "password", "f7": "review", "f8": "operation", "f9": "menu", "f10": "quit"}

func (m *model) contextActions() []menuEntry {
	switch m.screen {
	case access:
		label := "v Show password"
		if m.showSecrets {
			label = "v Hide password"
		}
		return []menuEntry{{label, "toggle-secrets"}}
	case installationChecks:
		return []menuEntry{{"v Check readiness", "install-check"}}
	case plan:
		if m.available("review") {
			return []menuEntry{{"F7 Review plan", "review"}}
		}
	case review:
		if m.available("apply") {
			return []menuEntry{{"a Apply…", "apply"}}
		}
	case operation:
		var items []menuEntry
		if m.available("resume") {
			items = append(items, menuEntry{"s Resume…", "resume"})
		}
		if m.available("cancel") {
			items = append(items, menuEntry{"c Cancel…", "cancel"})
		}
		return items
	}
	return nil
}

func (m *model) contextHint() string {
	if actions := m.contextActions(); len(actions) > 0 {
		text := actions[0].label
		for _, item := range actions[1:] {
			text += " · " + item.label
		}
		return text
	}
	switch m.screen {
	case home:
		return "↑/↓ Choose · Enter Open"
	case status:
		return "↑/↓ Scroll · Tab Navigation"
	}
	return "↑/↓ Scroll · Esc Overview"
}

func (m *model) menuItems() []menuEntry {
	switch m.menu {
	case 0:
		return []menuEntry{{"Overview          F2", "home"}, {"Status            F3", "status"}, {"Plan              F4", "plan"}, {"Operation         F8", "operation"}, {"Access details", "access"}}
	case 1:
		return []menuEntry{{"New installation", "install"}, {"Panel password    F6", "password"}, {"Review plan       F7", "review"}, {"Apply review", "apply"}, {"Resume operation", "resume"}, {"Cancel preparation", "cancel"}}
	default:
		return []menuEntry{{"Refresh           F5", "refresh"}, {"Keyboard help     F1", "help"}, {"Quit             F10", "quit"}}
	}
}

func (m *model) available(action string) bool {
	switch action {
	case "access":
		_, ok := m.backend.(control.AccessReader)
		return ok && m.supports("access") && m.state.report.Baseline
	case "toggle-secrets":
		return m.screen == access && m.accessInfo != nil
	case "install", "install-check":
		return m.installer() != nil && !m.state.report.Baseline && (m.state.operation.Status == "" || m.state.operation.Status == "idle" || m.state.operation.Status == "cancelled")
	case "password":
		return m.supports("configure", "apply")
	case "review":
		return m.state.plan.Status == "proposed" && m.supports("review")
	case "apply":
		return m.review != nil && m.review.CanApply && time.Now().Before(m.review.ExpiresAt) && m.supports("apply")
	case "resume":
		op := m.state.operation
		return !m.busy && op.ID != "" && op.Status != "complete" && op.Status != "cancelled" && m.supports("resume")
	case "cancel":
		op := m.state.operation
		return !m.busy && op.ID != "" && (op.Status == "preparing" || op.Status == "ready" && op.Completed == 0) && m.supports("cancel-preparation")
	}
	return true
}

func (m *model) dispatch(action string) tea.Cmd {
	if action == "apply" && m.review != nil && !time.Now().Before(m.review.ExpiresAt) {
		m.review = nil
		m.navigate(plan)
		m.message = "Review expired. Create a new review."
		return nil
	}
	if !m.available(action) {
		return nil
	}
	m.menuOpen = false
	switch action {
	case "access":
		m.navigate(access)
		return m.loadAccess()
	case "toggle-secrets":
		m.showSecrets = !m.showSecrets
		m.setContent(m.content())
	case "install":
		if m.state.report.Managed {
			m.navigate(installationChecks)
			return m.prepareInstallation(nil)
		}
		return m.startWizard()
	case "install-check":
		return m.prepareInstallation(nil)
	case "home":
		m.navigate(home)
	case "status":
		m.navigate(status)
	case "plan":
		m.navigate(plan)
	case "operation":
		m.navigate(operation)
	case "password":
		m.navigate(password)
		return m.fields[0].Focus()
	case "review":
		m.navigate(plan)
		return m.perform("review", "", "")
	case "apply":
		if m.screen != review {
			m.navigate(review)
		}
		m.confirmAction, m.confirmID = "apply", m.review.ID
		m.navigate(confirmation)
	case "resume", "cancel":
		if m.screen != operation {
			m.navigate(operation)
		}
		m.confirmAction, m.confirmID = action, m.state.operation.ID
		m.navigate(confirmation)
	case "refresh":
		if m.screen == access {
			return m.loadAccess()
		}
		if m.screen == review {
			m.navigate(plan)
		}
		m.message = "Refreshing…"
		return m.refresh()
	case "help":
		m.showSecrets = false
		m.setContent(m.content())
		m.helpBase, m.screen = m.screen, help
	case "menu":
		m.menuOpen, m.menuItem = true, 0
		if !m.available(m.menuItems()[0].action) {
			m.moveMenu(1)
		}
	case "quit":
		m.navigate(home)
		return tea.Quit
	}
	return nil
}

func (m *model) menuKey(key string) tea.Cmd {
	if key != "f9" {
		if action, ok := functionActions[key]; ok {
			return m.dispatch(action)
		}
	}
	switch key {
	case "esc", "f9":
		m.menuOpen = false
	case "left":
		m.menu, m.menuItem = (m.menu+2)%3, 0
		if !m.available(m.menuItems()[0].action) {
			m.moveMenu(1)
		}
	case "right", "tab":
		m.menu, m.menuItem = (m.menu+1)%3, 0
		if !m.available(m.menuItems()[0].action) {
			m.moveMenu(1)
		}
	case "up":
		m.moveMenu(-1)
	case "down":
		m.moveMenu(1)
	case "enter":
		return m.dispatch(m.menuItems()[m.menuItem].action)
	}
	return nil
}

func (m *model) moveMenu(delta int) {
	items := m.menuItems()
	for range len(items) {
		m.menuItem = (m.menuItem + delta + len(items)) % len(items)
		if m.available(items[m.menuItem].action) {
			return
		}
	}
}

package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer func() {
		if m.screen != password && m.screen != confirmation && m.screen != help {
			m.setContent(m.content())
		}
	}()
	switch msg := msg.(type) {
	case accessResult:
		m.busy = false
		if m.screen == access {
			if msg.err != nil {
				m.message = "Could not read applied access details. Check installation state."
			} else {
				m.accessInfo = &msg.report
			}
		}
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
	case snapshot:
		m.busy = false
		if msg.err != nil {
			m.message = "Refresh failed. Reconnect if the controller is unavailable."
		} else {
			m.state = msg
			if m.accessAfterRefresh {
				m.accessAfterRefresh = false
				if m.available("access") {
					return m, m.dispatch("access")
				}
			}
			if m.installer() != nil && msg.operation.ID != "" && msg.operation.Status != "complete" && msg.operation.Status != "cancelled" && m.screen == home {
				m.navigate(operation)
				return m, nil
			}
			if m.installer() != nil && !msg.report.Managed && (msg.operation.Status == "idle" || msg.operation.Status == "") && len(m.wizard.fields) == 0 && m.screen == home {
				return m, m.startWizard()
			}
			if m.message == "Refreshing…" || m.message == "Refresh failed. Reconnect if the controller is unavailable." {
				m.message = ""
			}
		}
	case installationResult:
		m.busy, m.mutating = false, false
		if msg.initialized {
			m.clearWizard()
			m.state.report.Managed = true
		}
		m.installPreflight = msg.preflight
		if msg.err != nil {
			if msg.initialized || m.screen != installationForm {
				m.navigate(installationChecks)
			}
			m.message = "Could not prepare or review installation. Check input, target ownership, preflight and installed release."
			return m, nil
		}
		m.clearWizard()
		if msg.review.ID != "" {
			m.review = &msg.review
			m.state.plan = msg.review.Plan
			m.navigate(review)
			m.message = "Review the installation effects before choosing Apply."
		} else {
			m.navigate(installationChecks)
		}
	case operationProgress:
		if m.busy && m.mutating {
			if msg.status.ID != "" {
				m.state.operation = msg.status
			}
			return m, m.pollOperation()
		}
	case actionResult:
		m.busy, m.mutating = false, false
		if msg.err != nil {
			m.review = nil
			m.message = "Request did not complete. Inspect Operation before retrying."
			m.navigate(operation)
			return m, m.refresh()
		}
		if msg.action == "review" {
			m.review = &msg.review
			m.navigate(review)
			m.message = "Review the effects before choosing Apply."
		} else {
			m.review = nil
			if msg.action == "configure" {
				m.navigate(plan)
				m.message = "Desired password saved. Review this plan before applying."
			} else {
				m.state.operation = msg.operation
				m.navigate(operation)
				m.message = "Operation: " + msg.operation.Status
				m.accessAfterRefresh = msg.operation.Kind == "xui-install-v1" && msg.operation.Status == "complete"
			}
			return m, m.refresh()
		}
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			m.clearPassword()
			m.clearWizard()
			m.navigate(home)
			return m, tea.Quit
		}
		if m.width < 40 || m.height < 18 {
			if key == "q" {
				return m, tea.Quit
			}
			return m, nil
		}
		if m.busy {
			if key == "q" && !m.mutating {
				return m, tea.Quit
			}
			return m, nil
		}
		if m.screen == installationForm {
			return m, m.wizardKey(msg)
		}
		if m.screen == password {
			return m, m.passwordKey(msg)
		}
		if m.screen == help {
			if key == "enter" || key == "esc" || key == "f1" {
				m.screen = m.helpBase
			}
			return m, nil
		}
		if m.menuOpen {
			return m, m.menuKey(key)
		}
		if key == "q" {
			m.navigate(home)
			return m, tea.Quit
		}
		if key == "esc" {
			switch m.screen {
			case review:
				m.navigate(plan)
			case confirmation:
				m.navigate(m.modalBase)
			default:
				m.navigate(home)
			}
			return m, nil
		}
		if m.screen == confirmation {
			if key == "tab" || key == "shift+tab" || key == "left" || key == "right" {
				m.confirmBack = !m.confirmBack
				return m, nil
			}
			if key == "enter" {
				if m.confirmBack {
					m.navigate(m.modalBase)
					return m, nil
				}
				action, id := m.confirmAction, m.confirmID
				m.confirmAction, m.confirmID = "", ""
				if action == "apply" && (m.review == nil || !m.review.CanApply || m.review.ID != id || !time.Now().Before(m.review.ExpiresAt)) {
					m.review = nil
					m.message = "Review expired. Create a new review."
					m.navigate(plan)
					return m, nil
				}
				m.navigate(operation)
				return m, m.perform(action, id, "")
			}
			return m, nil
		}
		if action, ok := functionActions[key]; ok {
			return m, m.dispatch(action)
		}
		if key == "alt+w" || key == "alt+a" || key == "alt+s" {
			m.menu = map[string]int{"alt+w": 0, "alt+a": 1, "alt+s": 2}[key]
			return m, m.dispatch("menu")
		}
		if key == "r" {
			return m, m.dispatch("refresh")
		}
		if key == "tab" || key == "shift+tab" {
			if m.width < 76 {
				m.navigate(home)
			} else {
				m.navFocused = !m.navFocused
			}
			return m, nil
		}
		if m.navFocused || m.screen == home {
			count := len(m.navigationScreens())
			switch key {
			case "up", "k":
				m.selected = (m.selected + count - 1) % count
			case "down", "j":
				m.selected = (m.selected + 1) % count
			case "enter":
				return m, m.openNavigation(m.selected)
			case "1", "2", "3", "4", "5", "6":
				index := int(key[0] - '1')
				if index < count {
					return m, m.openNavigation(index)
				}
			}
			return m, nil
		}
		switch m.screen {
		case access:
			if key == "v" {
				return m, m.dispatch("toggle-secrets")
			}
		case installationChecks:
			if key == "v" {
				return m, m.prepareInstallation(nil)
			}
		case plan:
			if key == "v" {
				return m, m.dispatch("review")
			}
		case review:
			if key == "a" {
				return m, m.dispatch("apply")
			}
		case operation:
			if key == "s" {
				return m, m.dispatch("resume")
			}
			if key == "c" {
				return m, m.dispatch("cancel")
			}
		}
	}
	if m.screen == installationForm && !m.busy && m.wizard.step < len(m.wizard.fields) && m.wizard.focus == 0 {
		var cmd tea.Cmd
		m.wizard.fields[m.wizard.step], cmd = m.wizard.fields[m.wizard.step].Update(msg)
		return m, cmd
	}
	if m.screen == password && m.focus < 2 && !m.busy {
		var cmd tea.Cmd
		m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
		return m, cmd
	}
	if m.screen != home && m.screen != password && m.screen != confirmation && m.screen != help {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) passwordKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.navigate(m.modalBase)
		return nil
	case "tab", "shift+tab":
		if m.focus < 2 {
			m.fields[m.focus].Blur()
		}
		delta := 1
		if msg.String() == "shift+tab" {
			delta = 3
		}
		m.focus = (m.focus + delta) % 4
		if m.focus < 2 {
			return m.fields[m.focus].Focus()
		}
		return nil
	case "left", "right":
		if m.focus >= 2 {
			m.focus = 5 - m.focus
			return nil
		}
	case "enter":
		if m.focus == 3 {
			m.navigate(m.modalBase)
			return nil
		}
		if m.focus == 0 {
			m.fields[0].Blur()
			m.focus = 1
			return m.fields[1].Focus()
		}
		value := m.fields[0].Value()
		if value == "" || len(value) > 4096 || value != m.fields[1].Value() {
			m.message = "Enter matching passwords (1–4096 bytes)."
			return nil
		}
		if !m.supports("configure", "apply") {
			m.clearPassword()
			return nil
		}
		m.clearPassword()
		return m.perform("configure", "", value)
	}
	if m.focus < 2 {
		var cmd tea.Cmd
		m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
		return cmd
	}
	return nil
}

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type dialogButton struct {
	label, action string
	focused       bool
}

// Text and hit regions are built together, including wrapped buttons at narrow
// widths. Mouse coordinates never guess the position of a rendered control.
func (m *model) dialog() (string, []hitRegion) {
	width := min(m.width-4, 64)
	inner := width - 4
	lines := []string{""}
	var hits []hitRegion
	add := func(text string) int {
		y := len(lines) + 1
		lines = append(lines, strings.Split(ansi.Wrap(text, inner, ""), "\n")...)
		return y
	}
	buttons := func(items ...dialogButton) {
		x, row := 0, ""
		y := len(lines) + 1
		for _, item := range items {
			label := "  [ " + item.label + " ]"
			if item.focused {
				label = "> [ " + item.label + " ]"
			}
			w := ansi.StringWidth(label)
			if x > 0 && x+w > inner {
				add(row)
				row = ""
				x = 0
				y = len(lines) + 1
			}
			hits = append(hits, hitRegion{2 + x, y, w, 1, item.action, 0})
			if item.focused {
				label = selected.Render(label)
			}
			row += label + " "
			x += w + 1
		}
		add(strings.TrimRight(row, " "))
	}
	switch m.screen {
	case installationForm:
		w := &m.wizard
		summary := w.step == len(w.fields)
		if !summary {
			position, total := w.step+1, len(w.fields)
			if w.fields[4].Value() != "dns" {
				total--
				if w.step > 5 {
					position--
				}
			}
			add(fmt.Sprintf("Step %d / %d", position, total))
			add(wizardFields[w.step].label)
			y := add("[ " + cell(w.fields[w.step].View(), inner-4) + " ]")
			hits = append(hits, hitRegion{2, y, inner, 1, "wizard-field", 0})
			add("")
			add(wizardFields[w.step].hint)
		} else {
			add(cell("Target: "+clean(m.installer().InstallationTarget()), inner))
			for _, i := range []int{0, 1, 2} {
				label := []string{"Base", "Website", "Panel"}[i]
				add(cell(label+": "+clean(w.fields[i].Value()), inner))
			}
			add("Reality · Trojan · Hysteria2")
			add("Credentials stay private. Deploy follows review.")
		}
		add("")
		next := "Next"
		offset := 1
		if summary {
			next = "Prepare draft"
			offset = 0
		}
		buttons(dialogButton{next, "wizard-next", w.focus == offset}, dialogButton{"Back", "wizard-back", w.focus == offset+1}, dialogButton{"Cancel", "wizard-cancel", w.focus == offset+2})
	case password:
		for i, label := range []string{"New password", "Repeat password"} {
			add(label)
			field := "[ " + cell(m.fields[i].View(), inner-4) + " ]"
			if m.focus == i {
				field = accent.Render(field)
			}
			y := add(field)
			hits = append(hits, hitRegion{2, y, inner, 1, "field", i})
			add("")
		}
		add(muted.Render("Review and Apply follow separately."))
		add("")
		buttons(dialogButton{"Save desired", "save", m.focus == 2}, dialogButton{"Cancel", "close", m.focus == 3})
	case confirmation:
		verb := map[string]string{"apply": "Apply", "resume": "Resume", "cancel": "Cancel preparation"}[m.confirmAction]
		question := map[string]string{"apply": "Apply the reviewed changes?", "resume": "Resume from the saved checkpoint?", "cancel": "Cancel preparation before effects?"}[m.confirmAction]
		if m.confirmAction == "apply" && m.review != nil && m.review.Kind == "xui-install-v1" {
			verb, question = "Deploy", "Deploy the reviewed stack and start its services?"
		}
		add(question)
		add("")
		add("ID: " + clean(m.confirmID))
		add("")
		buttons(dialogButton{verb, "confirm", !m.confirmBack}, dialogButton{"Back", "close", m.confirmBack})
	case help:
		add("F2  Overview     F3  Status\nF4  Plan         F5  Refresh\nF6  Password     F7  Review plan\nF8  Operation    F9  Main menu\nF10 Quit\n\nArrows / PgUp / PgDn: scroll\nTab: switch pane or form control\nEsc: close menu or dialog")
		add("")
		buttons(dialogButton{"Close", "close", true})
	}
	height := min(m.height-3, len(lines)+3)
	// Discard clipped controls; layout tests also require action buttons to fit.
	visible := hits[:0]
	for _, hit := range hits {
		if hit.y < height-1 {
			visible = append(visible, hit)
		}
	}
	return panel(screenNames[m.screen], inset(strings.Join(lines, "\n")), width, height), visible
}

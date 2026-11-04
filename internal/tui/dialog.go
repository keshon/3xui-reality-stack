package tui

import (
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
		question := map[string]string{"apply": "Apply the reviewed password change?", "resume": "Resume from the saved checkpoint?", "cancel": "Cancel preparation before effects?"}[m.confirmAction]
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

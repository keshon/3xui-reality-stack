package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestIndependentReviewDialogRegionsMatchVisibleControls(t *testing.T) {
	for _, size := range [][2]int{{40, 18}, {41, 19}, {75, 24}, {76, 18}, {80, 24}, {121, 40}} {
		for _, page := range []screen{password, confirmation, help} {
			m, _ := readyModel(t, true)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.review = &deployment.Review{ID: strings.Repeat("a", 32), CanApply: true, ExpiresAt: time.Now().Add(time.Minute)}
			m.confirmAction, m.confirmID = "cancel", strings.Repeat("c", 32)
			m.navigate(plan)
			if page == help {
				m.dispatch("help")
			} else {
				m.navigate(page)
			}
			view, hits := m.compose()
			rows := strings.Split(ansi.Strip(view.Content), "\n")
			for _, hit := range hits {
				if hit.y < 0 || hit.y >= len(rows) || hit.x < 0 || hit.x+hit.width > size[0] {
					t.Fatalf("out of bounds hit at %v page %d: %+v", size, page, hit)
				}
				text := ansi.Cut(rows[hit.y], hit.x, hit.x+hit.width)
				if !strings.Contains(text, "[") || !strings.Contains(text, "]") {
					t.Fatalf("hit does not match control at %v page %d: %+v text=%q", size, page, hit, text)
				}
			}
		}
	}
}

func TestIndependentReviewModalBackgroundClicksCannotWriteOrNavigate(t *testing.T) {
	for _, page := range []screen{password, confirmation, help} {
		m, backend := readyModel(t, true)
		m.navigate(plan)
		m.review = &deployment.Review{ID: "review", CanApply: true, ExpiresAt: time.Now().Add(time.Minute)}
		m.confirmAction, m.confirmID = "apply", "review"
		if page == help {
			m.dispatch("help")
		} else {
			m.navigate(page)
		}
		for _, point := range [][2]int{{10, 0}, {1, 3}, {45, 23}, {79, 0}} {
			_, cmd := m.Update(tea.MouseClickMsg{X: point[0], Y: point[1], Button: tea.MouseLeft})
			if cmd != nil || m.screen != page || backend.applyCalls+backend.configurationCalls+backend.reviewCalls != 0 {
				t.Fatalf("background click escaped modal %d at %v", page, point)
			}
		}
	}
}

//go:build !windows

package tui

import (
	"context"
	"os"

	tea "charm.land/bubbletea/v2"
)

func consoleInput(context.Context, *os.File, *os.File) ([]tea.ProgramOption, func(), error) {
	return nil, func() {}, nil
}

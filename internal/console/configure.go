package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keshon/3xui-reality-stack/internal/control"
)

// ReadPassword accepts a pipe/file, never a command-line secret. One final
// LF/CRLF is removed; other whitespace is literal. Input ends at EOF.
func ReadPassword(in io.Reader) (string, error) {
	if in == nil {
		return "", errors.New("password input is required on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(in, 4099))
	if err != nil || len(data) > 4098 || !utf8.Valid(data) {
		return "", errors.New("cannot read a valid password within the input limit")
	}
	password := string(data)
	if strings.HasSuffix(password, "\n") {
		password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
	}
	if len(password) == 0 || len(password) > 4096 {
		return "", errors.New("password must contain between 1 and 4096 UTF-8 bytes")
	}
	for _, r := range password {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", errors.New("password cannot contain control characters or line separators")
		}
	}
	return password, nil
}

func ConfigurePassword(ctx context.Context, password string, out io.Writer, editor control.Configurator) error {
	plan, err := editor.Configure(ctx, map[string]string{"PANEL_PASS": password})
	if err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(plan)
}

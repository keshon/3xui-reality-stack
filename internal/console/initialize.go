package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/keshon/3xui-reality-stack/internal/control"
)

func ReadInitialization(in io.Reader) (json.RawMessage, error) {
	if in == nil {
		return nil, errors.New("initial configuration JSON is required on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(in, (32<<10)+1))
	if err != nil || len(data) > 32<<10 || !utf8.Valid(data) || !json.Valid(data) {
		return nil, errors.New("initial configuration must be valid UTF-8 JSON within 32 KiB")
	}
	return data, nil
}

func Initialize(ctx context.Context, input json.RawMessage, out io.Writer, initializer control.Initializer) error {
	report, err := initializer.Initialize(ctx, input)
	if err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(report)
}

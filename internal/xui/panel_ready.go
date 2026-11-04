package xui

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Probe the pinned unauthenticated endpoint before bootstrap writes. A Docker
// start/restart receipt does not mean the HTTP listener is accepting requests.
func waitPanelHTTP(ctx context.Context, p *Panel) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	s := &panelSession{base: p.base, http: &http.Client{Transport: p.transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var token string
		if err := s.json(ctx, "/csrf-token", nil, &token); err == nil && token != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("private panel HTTP readiness timed out")
		case <-ticker.C:
		}
	}
}

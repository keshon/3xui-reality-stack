// Command stack-manager is the experimental remote CLI and fullscreen TUI.
// It uses the controller client over SSH and imports no stack adapters.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/sshclient"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// Interactive sessions outlive a single request. Handshake and TUI requests
	// have their own budgets; batch commands keep the original total deadline.
	if len(os.Args) < 2 || os.Args[1] != "tui" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	if err := runWithInput(ctx, os.Args[1:], os.Stdin, os.Stdout, sshclient.Connect); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

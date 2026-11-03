// Command stack-manager is the experimental remote CLI. It has no
// stack adapters; the same controller client will serve the future manager TUI.
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
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := runWithInput(ctx, os.Args[1:], os.Stdin, os.Stdout, sshclient.Connect); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

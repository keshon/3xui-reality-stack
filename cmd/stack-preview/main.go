// Command stack-preview runs the real fullscreen UI with an in-memory backend.
// It is a development tool, excluded from controller and manager release assets.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/keshon/3xui-reality-stack/internal/tui"
)

func main() {
	scenario := flag.String("scenario", "healthy", "healthy, blocked, or interrupted")
	flag.Parse()
	b, err := newPreview(*scenario)
	if flag.NArg() != 0 {
		err = fmt.Errorf("unexpected arguments")
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = tui.Run(ctx, b, os.Stdin, os.Stdout, "MOCK DATA · "+*scenario+" · nothing is saved or deployed")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

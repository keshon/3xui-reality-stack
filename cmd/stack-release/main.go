// Command stack-release builds experimental release assets; it never publishes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/keshon/3xui-reality-stack/internal/release"
)

func main() {
	f := flag.NewFlagSet("stack-release", flag.ContinueOnError)
	version := f.String("version", "", "release version, for example v0.1.0-go.1")
	output := f.String("out", "", "new output directory with an existing parent")
	if f.Parse(os.Args[1:]) != nil || f.NArg() != 0 {
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := release.Build(ctx, *version, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "Release assets and SHA256SUMS built; nothing published.")
}

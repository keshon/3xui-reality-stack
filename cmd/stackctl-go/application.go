package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/keshon/3xui-reality-stack/internal/buildinfo"
	"github.com/keshon/3xui-reality-stack/internal/console"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/tui"
	"github.com/keshon/3xui-reality-stack/internal/xui"
)

// The command consumes a small interface; production wiring selects 3x-ui.
// Tests supply a second stack without adding a fake backend to the executable.
type stackInspector interface {
	Inspect(context.Context, string) (deployment.Inspection, error)
	Runtime(context.Context, string) (deployment.RuntimeReport, error)
}

func run(args []string, out io.Writer) error {
	return runContext(context.Background(), args, os.Stdin, out)
}

func runContext(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	return runApplication(ctx, args, in, out, xui.Stack{})
}

func runWithStack(ctx context.Context, args []string, out io.Writer, stack stackInspector) error {
	return runApplication(ctx, args, nil, out, stack)
}

func runApplication(ctx context.Context, args []string, in io.Reader, out io.Writer, stack stackInspector) error {
	const usage = "usage: stackctl-go version\n       stackctl-go tui [--root DIRECTORY] [--experimental-password-writes]\n       stackctl-go initialize --config-stdin --root /absolute/new/directory\n       stackctl-go <inspect|plan|runtime|preflight|review|operation> [--root DIRECTORY]\n       stackctl-go configure-password --password-stdin --experimental-password-writes [--root DIRECTORY]\n       stackctl-go apply --confirm REVIEW_ID [--root DIRECTORY]\n       stackctl-go <resume|cancel-preparation> --operation OPERATION_ID [--root DIRECTORY]\n       stackctl-go serve --stdio [--root DIRECTORY]\n3x-ui password configuration and execution require --experimental-password-writes.\nFresh standalone installation requires --experimental-install on plan/review/apply/resume/cancel-preparation; local CLI only."
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "export-certificates" {
		return exportCertificates(ctx, args[1:], out)
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		_, err := fmt.Fprintln(out, buildinfo.Version)
		return err
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(out, usage+"\n"+certificateUsage)
		return err
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard) // Flag errors may echo arbitrary argument values.
	root := f.String("root", ".", "deployment directory")
	stdio := f.Bool("stdio", false, "private stdio controller protocol")
	confirm := f.String("confirm", "", "explicitly approve this exact review identifier")
	operation := f.String("operation", "", "exact operation identifier for recovery")
	install := f.Bool("experimental-install", false, "enable the experimental standalone installation workflow")
	passwordWrites := f.Bool("experimental-password-writes", false, "enable the experimental 3x-ui password workflow")
	passwordStdin := f.Bool("password-stdin", false, "read the desired password from stdin through EOF")
	configStdin := f.Bool("config-stdin", false, "read private initialization JSON from stdin through EOF")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		return errors.New(usage)
	}
	if *passwordStdin != (args[0] == "configure-password") {
		return errors.New(usage)
	}
	if *configStdin != (args[0] == "initialize") {
		return errors.New(usage)
	}
	if *install {
		if *passwordWrites {
			return errors.New(usage)
		}
		switch args[0] {
		case "plan", "review", "apply", "resume", "cancel-preparation":
		default:
			return errors.New(usage)
		}
		adapter, ok := stack.(xui.Stack)
		if !ok {
			return errors.New("installation execution option requires the 3x-ui adapter")
		}
		stack = xui.InstallStack{Stack: adapter}
	}
	if *passwordWrites {
		switch args[0] {
		case "configure-password", "review", "apply", "resume", "cancel-preparation", "serve", "tui":
		default:
			return errors.New(usage)
		}
		adapter, ok := stack.(xui.Stack)
		if !ok {
			return errors.New("password execution option requires the 3x-ui adapter")
		}
		stack = xui.PasswordStack{Stack: adapter}
	}
	controller := control.Controller{Root: *root, Stack: stack}
	if args[0] == "tui" {
		if *stdio || *confirm != "" || *operation != "" {
			return errors.New(usage)
		}
		return tui.Run(ctx, controller, in, out, "Local controller")
	}
	if args[0] == "initialize" {
		if *stdio || *confirm != "" || *operation != "" {
			return errors.New(usage)
		}
		input, err := console.ReadInitialization(in)
		if err != nil {
			return err
		}
		return console.Initialize(ctx, input, out, controller)
	}
	if args[0] == "configure-password" {
		if !*passwordWrites || *stdio || *confirm != "" || *operation != "" {
			return errors.New(usage)
		}
		password, err := console.ReadPassword(in)
		if err != nil {
			return err
		}
		return console.ConfigurePassword(ctx, password, out, controller)
	}
	if args[0] == "serve" {
		if !*stdio || in == nil || *confirm != "" || *operation != "" {
			return errors.New(usage)
		}
		return control.Serve(ctx, in, out, controller)
	}
	if *stdio {
		return errors.New(usage)
	}
	return console.Run(ctx, args[0], console.Options{Confirm: *confirm, OperationID: *operation}, out, controller)
}

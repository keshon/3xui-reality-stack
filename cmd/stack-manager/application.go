package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/buildinfo"
	"github.com/keshon/3xui-reality-stack/internal/console"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/sshclient"
	"github.com/keshon/3xui-reality-stack/internal/tui"
)

type connector func(context.Context, sshclient.Options) (io.ReadWriteCloser, error)

func run(ctx context.Context, args []string, out io.Writer, connect connector) error {
	return runWithInput(ctx, args, nil, out, connect)
}

func runWithInput(ctx context.Context, args []string, in io.Reader, out io.Writer, connect connector) error {
	const usage = "usage: stack-manager version\n       stack-manager <tui|initialize|inspect|plan|runtime|preflight|review|operation|configure-password|apply|resume|cancel-preparation> --ssh ALIAS --root /absolute/deployment/path [--controller /absolute/binary/path]\ninitialize requires --config-stdin; configure-password requires --password-stdin; apply requires --confirm REVIEW_ID; recovery requires --operation OPERATION_ID.\n3x-ui password configuration and execution require --experimental-password-writes."
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		_, err := fmt.Fprintln(out, buildinfo.Version)
		return err
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, usage)
		return err
	}
	if len(args) == 0 {
		return errors.New(usage)
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	alias := f.String("ssh", "", "configured SSH alias")
	root := f.String("root", "", "absolute deployment path on the VPS")
	binary := f.String("controller", "/usr/local/bin/stackctl-go", "absolute controller binary path on the VPS")
	confirm := f.String("confirm", "", "explicitly approve this exact review identifier")
	operation := f.String("operation", "", "exact operation identifier for recovery")
	passwordWrites := f.Bool("experimental-password-writes", false, "request the experimental password controller over SSH")
	passwordStdin := f.Bool("password-stdin", false, "read the desired password from stdin through EOF")
	configStdin := f.Bool("config-stdin", false, "read private initialization JSON from stdin through EOF")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *alias == "" || *root == "" {
		return errors.New(usage)
	}
	options := console.Options{Confirm: *confirm, OperationID: *operation}
	if *configStdin != (args[0] == "initialize") {
		return errors.New(usage)
	}
	if *passwordStdin != (args[0] == "configure-password") {
		return errors.New(usage)
	}
	var password string
	var initial json.RawMessage
	if args[0] == "initialize" {
		if *confirm != "" || *operation != "" || *passwordWrites {
			return errors.New(usage)
		}
		var err error
		initial, err = console.ReadInitialization(in)
		if err != nil {
			return err
		}
	} else if args[0] == "configure-password" {
		if !*passwordWrites || *confirm != "" || *operation != "" {
			return errors.New(usage)
		}
		var err error
		password, err = console.ReadPassword(in)
		if err != nil {
			return err
		}
	} else if args[0] == "tui" {
		if *confirm != "" || *operation != "" {
			return errors.New(usage)
		}
	} else {
		if err := options.Validate(args[0]); err != nil {
			return err
		}
	}
	if *passwordWrites {
		switch args[0] {
		case "configure-password", "review", "apply", "resume", "cancel-preparation", "tui":
		default:
			return errors.New(usage)
		}
	}
	stream, err := connect(ctx, sshclient.Options{Alias: *alias, Root: *root, Controller: *binary, ExperimentalPasswordWrites: *passwordWrites})
	if err != nil {
		return err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	client, err := control.Connect(handshakeCtx, stream)
	cancel()
	if err != nil {
		return err
	}
	defer client.Close()
	if args[0] == "tui" {
		return tui.Run(ctx, client, in, out, "SSH: "+*alias)
	}
	if args[0] == "initialize" {
		return console.Initialize(ctx, initial, out, client)
	}
	if args[0] == "configure-password" {
		return console.ConfigurePassword(ctx, password, out, client)
	}
	return console.Run(ctx, args[0], options, out, client)
}

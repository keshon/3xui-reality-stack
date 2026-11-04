// Package sshclient opens a controller's stdio channel through the local OpenSSH
// client. SSH configuration, agents and known hosts remain owned by OpenSSH.
package sshclient

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
)

type Options struct {
	Alias                      string
	Controller                 string
	Root                       string
	ExperimentalPasswordWrites bool
}

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]*$`)

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func remoteCommand(o Options) (string, error) {
	if !aliasPattern.MatchString(o.Alias) {
		return "", errors.New("use a configured SSH alias or user@host")
	}
	for _, value := range []string{o.Controller, o.Root} {
		if !path.IsAbs(value) {
			return "", errors.New("controller and deployment paths must be absolute Linux paths")
		}
		for _, r := range value {
			if r < 32 || r == 127 {
				return "", errors.New("invalid remote path")
			}
		}
	}
	remote := "exec " + shellQuote(o.Controller) + " serve --stdio --root " + shellQuote(o.Root)
	if o.ExperimentalPasswordWrites {
		remote += " --experimental-password-writes"
	}
	return remote, nil
}

type commandFactory func(context.Context, string, string) *exec.Cmd

func command(ctx context.Context, alias, remote string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh", "-T", "-a", "-x", "-o", "ClearAllForwardings=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "--", alias, remote)
}

func Connect(ctx context.Context, options Options) (io.ReadWriteCloser, error) {
	return connect(ctx, options, command)
}

func connect(ctx context.Context, options Options, factory commandFactory) (io.ReadWriteCloser, error) {
	remote, err := remoteCommand(options)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	cmd := factory(childCtx, options.Alias, remote)
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, errors.New("cannot open SSH controller input")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		cancel()
		return nil, errors.New("cannot open SSH controller output")
	}
	// Remote banners/errors are untrusted and may contain deployment secrets.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		cancel()
		return nil, errors.New("cannot start SSH; check the local OpenSSH installation and configuration")
	}
	c := &connection{in: in, out: out, cancel: cancel, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(c.done) }()
	return c, nil
}

type connection struct {
	in     io.WriteCloser
	out    io.ReadCloser
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (c *connection) Read(data []byte) (int, error)  { return c.out.Read(data) }
func (c *connection) Write(data []byte) (int, error) { return c.in.Write(data) }
func (c *connection) Close() error {
	c.once.Do(func() { _ = c.in.Close(); _ = c.out.Close(); c.cancel(); <-c.done })
	return nil
}

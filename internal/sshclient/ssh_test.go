package sshclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/testkit"
)

func TestCommandUsesVerifiedNoninteractiveSSHAndQuotedPaths(t *testing.T) {
	o := Options{Alias: "root@vps-test", Controller: "/opt/my controller's/stack", Root: "/opt/stack $HOME; $(touch nope) `id`"}
	remote, err := remoteCommand(o)
	if err != nil {
		t.Fatal(err)
	}
	want := "exec '/opt/my controller'\\''s/stack' serve --stdio --root '/opt/stack $HOME; $(touch nope) `id`'"
	if remote != want {
		t.Fatal("remote shell arguments were not quoted independently")
	}
	o.ExperimentalPasswordWrites = true
	withWrites, err := remoteCommand(o)
	if err != nil || withWrites != want+" --experimental-password-writes" {
		t.Fatal("password execution choice did not reach the fixed remote command")
	}
	cmd := command(context.Background(), o.Alias, remote)
	if !reflect.DeepEqual(cmd.Args, []string{"ssh", "-T", "-a", "-x", "-o", "ClearAllForwardings=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "--", o.Alias, remote}) {
		t.Fatal("SSH invocation changed verification or stream handling")
	}
}

func TestUnsafeOptionsCannotStartProcess(t *testing.T) {
	for _, o := range []Options{
		{Alias: "-oProxyCommand=private", Controller: "/bin/stack", Root: "/opt/stack"},
		{Alias: "alias\nprivate", Controller: "/bin/stack", Root: "/opt/stack"},
		{Alias: "alias", Controller: "relative", Root: "/opt/stack"},
		{Alias: "alias", Controller: "/bin/stack", Root: "relative"},
		{Alias: "alias", Controller: "/bin/stack", Root: "/opt/stack\nprivate"},
	} {
		called := false
		_, err := connect(context.Background(), o, func(context.Context, string, string) *exec.Cmd { called = true; return nil })
		if err == nil || called || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe SSH options reached a process or leaked")
		}
	}
}

func TestSSHProcessHelper(t *testing.T) {
	root := os.Getenv("STACK_SSH_HELPER_ROOT")
	if root == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "private-server-debug")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := control.Serve(ctx, os.Stdin, os.Stdout, control.Controller{Root: root, Stack: testkit.Stack{}}); err != nil {
		os.Exit(24)
	}
	os.Exit(0)
}

func TestProcessTransportCarriesProtocolAndCloses(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "before", "after"); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	factory := func(ctx context.Context, alias, remote string) *exec.Cmd {
		if alias != "fixture-vps" || !strings.HasPrefix(remote, "exec '") {
			t.Fatal("SSH options not carried to process")
		}
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestSSHProcessHelper$")
		cmd.Env = append(os.Environ(), "STACK_SSH_HELPER_ROOT="+root)
		return cmd
	}
	stream, err := connect(ctx, Options{Alias: "fixture-vps", Controller: "/opt/stackctl-go", Root: "/opt/xray-stack"}, factory)
	if err != nil {
		t.Fatal(err)
	}
	client, err := control.Connect(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(ctx)
	if err != nil || plan.Stack != "fixture-site" || plan.Status != "proposed" {
		t.Fatal("process transport did not carry controller protocol")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Plan(ctx); err == nil {
		t.Fatal("closed process transport reused")
	}
}

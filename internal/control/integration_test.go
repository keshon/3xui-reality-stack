package control_test

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/console"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/testkit"
	"github.com/keshon/3xui-reality-stack/internal/xui"
)

func TestLocalAndRemoteUseSameControllerContract(t *testing.T) {
	for _, adapter := range []string{"fixture-site", "3x-ui"} {
		t.Run(adapter, func(t *testing.T) {
			root := t.TempDir()
			var stack control.Inspector = xui.Stack{}
			if adapter == "fixture-site" {
				if err := testkit.Seed(root, "private-current", "private-desired"); err != nil {
					t.Fatal(err)
				}
				stack = testkit.Stack{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			controller := control.Controller{Root: root, Stack: stack}
			left, right := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); defer right.Close(); _ = control.Serve(ctx, right, right, controller) }()
			client, err := control.Connect(ctx, left)
			if err != nil {
				left.Close()
				t.Fatal(err)
			}
			defer func() { client.Close(); <-done }()
			for _, command := range []string{"inspect", "plan", "runtime"} {
				var local, remote bytes.Buffer
				localErr := console.Read(ctx, command, &local, controller)
				remoteErr := console.Read(ctx, command, &remote, client)
				if (localErr == nil) != (remoteErr == nil) || local.String() != remote.String() {
					t.Fatal("local/remote controller behavior differs")
				}
				if strings.Contains(remote.String(), "private-current") || strings.Contains(remote.String(), "private-desired") {
					t.Fatal("fixture values crossed public API")
				}
			}
			if adapter == "fixture-site" {
				workflow, err := (testkit.Stack{}).Review(ctx, root)
				if err != nil {
					t.Fatal(err)
				}
				if err := (deployment.Engine{Root: root}).Start(ctx, workflow); err != nil {
					t.Fatal(err)
				}
				plan, err := client.Plan(ctx)
				if err != nil || plan.Status != "unchanged" {
					t.Fatal("remote client did not refresh after execution")
				}
			}
		})
	}
}

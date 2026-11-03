package control_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/console"
	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/testkit"
	"github.com/keshon/3xui-reality-stack/internal/xui"
)

func connectController(t *testing.T, controller control.Controller) (*control.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	left, right := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer right.Close(); _ = control.Serve(ctx, right, right, controller) }()
	client, err := control.Connect(ctx, left)
	if err != nil {
		left.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); cancel(); <-done })
	return client, ctx
}

func TestRemoteReviewConfirmationRecoveryAndDuplicateDelivery(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "private-before", "private-after"); err != nil {
		t.Fatal(err)
	}
	controller := control.Controller{Root: root, Stack: testkit.Stack{Fault: testkit.AfterWrite}}
	client, ctx := connectController(t, controller)
	review, err := client.Review(ctx)
	if err != nil || !review.CanApply {
		t.Fatalf("remote review failed: %v", err)
	}
	public, _ := json.Marshal(review)
	if strings.Contains(string(public), "private-") || strings.Contains(string(public), "binding") {
		t.Fatal("private review data crossed protocol")
	}
	if _, err := client.ApplyReview(ctx, review.ID, false); err == nil {
		t.Fatal("remote apply accepted missing confirmation")
	}
	if _, err := client.ApplyReview(ctx, review.ID, true); err == nil {
		t.Fatal("expected interrupted apply")
	}
	status, err := client.Operation(ctx)
	if err != nil || status.Status != "failed" || status.ReviewID != review.ID {
		t.Fatal("remote operation state unavailable")
	}
	repeated, err := client.ApplyReview(ctx, review.ID, true)
	if err != nil || repeated.ID != status.ID || repeated.Status != "failed" {
		t.Fatal("duplicate delivery repeated the action")
	}
	client.Close()
	// Reconnect to the same on-disk operation with a recovered application.
	controller.Stack = testkit.Stack{}
	client, ctx = connectController(t, controller)
	status, err = client.ResumeOperation(ctx, status.ID)
	if err != nil || status.Status != "complete" {
		t.Fatalf("remote resume failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".fixture", "effects.log"))
	if err != nil || string(data) != "configure-site\n" {
		t.Fatal("remote recovery duplicated effect")
	}
}

func TestRemoteStaleReviewHasActionableSafeError(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	client, ctx := connectController(t, control.Controller{Root: root, Stack: testkit.Stack{}})
	review, err := client.Review(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := deployment.WriteJSON(filepath.Join(root, ".fixture", "identity.json"), map[string]string{"value": "private-replacement"}); err != nil {
		t.Fatal(err)
	}
	_, err = client.ApplyReview(ctx, review.ID, true)
	if !errors.Is(err, deployment.ErrStaleReview) || strings.Contains(err.Error(), "private-") {
		t.Fatal("remote stale review was accepted or lost its safe classification")
	}
}

func TestThreeXUIExecutionIsNotAdvertisedOrEnabled(t *testing.T) {
	root := t.TempDir()
	controller := control.Controller{Root: root, Stack: xui.Stack{}}
	client, ctx := connectController(t, controller)
	if _, err := client.ApplyReview(ctx, strings.Repeat("a", 32), true); err == nil {
		t.Fatal("3x-ui write capability was enabled")
	}
	if _, err := controller.ApplyReview(ctx, strings.Repeat("a", 32), true); !errors.Is(err, deployment.ErrExecutionUnavailable) {
		t.Fatal("local 3x-ui write gate was bypassed")
	}
	if _, err := os.Stat(filepath.Join(root, ".stack")); !os.IsNotExist(err) {
		t.Fatal("disabled write touched deployment state")
	}
}

type dropWriter struct {
	out    io.Writer
	frames int
}

func (w *dropWriter) Write(data []byte) (int, error) {
	w.frames++
	if w.frames == 2 {
		return 0, io.ErrClosedPipe
	}
	return w.out.Write(data)
}

func TestLostApplyReplyCanBeResolvedAfterReconnect(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	controller := control.Controller{Root: root, Stack: testkit.Stack{}}
	review, err := controller.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	left, right := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer right.Close()
		_ = control.Serve(ctx, right, &dropWriter{out: right}, controller)
	}()
	client, err := control.Connect(ctx, left)
	if err != nil {
		left.Close()
		t.Fatal(err)
	}
	if _, err := client.ApplyReview(ctx, review.ID, true); err == nil {
		t.Fatal("lost reply was reported as acknowledged")
	}
	client.Close()
	<-done
	client, ctx = connectController(t, controller)
	status, err := client.Operation(ctx)
	if err != nil || status.Status != "complete" || status.ReviewID != review.ID {
		t.Fatal("reconnect could not resolve committed operation")
	}
	receipt, err := client.ApplyReview(ctx, review.ID, true)
	if err != nil || receipt.ID != status.ID {
		t.Fatal("lost-reply retry did not return the original receipt")
	}
	data, _ := os.ReadFile(filepath.Join(root, ".fixture", "effects.log"))
	if string(data) != "configure-site\n" {
		t.Fatal("lost reply caused duplicate application")
	}
}

func TestProtocolRejectsMissingConfirmationAndUnexpectedParams(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	controller := control.Controller{Root: root, Stack: testkit.Stack{}}
	review, err := controller.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, params := range []string{`{"review_id":"` + review.ID + `"}`, `{"review_id":"` + review.ID + `","confirmed":false}`, `{"review_id":"` + review.ID + `","confirmed":true,"root":"private-other-root"}`} {
		var out bytes.Buffer
		input := `{"version":1,"id":1,"method":"apply","params":` + params + "}\n"
		if err := control.Serve(context.Background(), strings.NewReader(input), &out, controller); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "invalid_params") || strings.Contains(out.String(), "private-") {
			t.Fatal("invalid confirmation or path parameter reached execution")
		}
	}
	status, err := controller.Operation(context.Background())
	if err != nil || status.Status != "idle" {
		t.Fatal("invalid parameters started an operation")
	}
}

func TestSharedConsoleReviewAndExactConfirmation(t *testing.T) {
	root := t.TempDir()
	if err := testkit.Seed(root, "old", "new"); err != nil {
		t.Fatal(err)
	}
	client, ctx := connectController(t, control.Controller{Root: root, Stack: testkit.Stack{}})
	var out bytes.Buffer
	if err := console.Run(ctx, "review", console.Options{}, &out, client); err != nil {
		t.Fatal(err)
	}
	var review deployment.Review
	if json.Unmarshal(out.Bytes(), &review) != nil {
		t.Fatal("invalid review output")
	}
	out.Reset()
	if err := console.Run(ctx, "apply", console.Options{Confirm: review.ID}, &out, client); err != nil {
		t.Fatal(err)
	}
	var status deployment.OperationStatus
	if json.NewDecoder(bufio.NewReader(&out)).Decode(&status) != nil || status.Status != "complete" {
		t.Fatal("console did not report completed operation")
	}
}

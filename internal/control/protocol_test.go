package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type testReader struct {
	failed  bool
	entered chan struct{}
	release chan struct{}
}

func (r testReader) Inspect(ctx context.Context) (deployment.Report, error) {
	if r.failed {
		return deployment.Report{}, errors.New("private-adapter-secret")
	}
	return deployment.Report{Stack: "fixture", Managed: true, Baseline: true, Blockers: []string{}}, nil
}
func (r testReader) Plan(ctx context.Context) (deployment.Plan, error) {
	if r.entered != nil {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return deployment.Plan{}, ctx.Err()
		}
	}
	return deployment.Plan{Stack: "fixture", Status: "unchanged", ReadOnly: true, Actions: []deployment.Action{}}, nil
}

func (r testReader) Runtime(context.Context) (deployment.RuntimeReport, error) {
	return deployment.RuntimeReport{Stack: "fixture", Status: "compatible", Checks: []deployment.Check{}}, nil
}

func pipeClient(t *testing.T, reader Reader) (*Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	left, right := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer right.Close(); _ = Serve(ctx, right, right, reader) }()
	client, err := Connect(ctx, left)
	if err != nil {
		cancel()
		left.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); cancel(); <-done })
	return client, ctx
}

func TestProtocolConcurrentCallsRemainCorrelated(t *testing.T) {
	client, ctx := pipeClient(t, testReader{})
	var group sync.WaitGroup
	for n := 0; n < 12; n++ {
		group.Go(func() {
			report, err := client.Inspect(ctx)
			if err != nil || report.Stack != "fixture" {
				t.Error("correlated inspect failed")
			}
			plan, err := client.Plan(ctx)
			if err != nil || plan.Status != "unchanged" {
				t.Error("correlated plan failed")
			}
		})
	}
	group.Wait()
}

func TestCancelledQueuedCallDoesNotKillActiveRequest(t *testing.T) {
	reader := testReader{entered: make(chan struct{}), release: make(chan struct{})}
	client, ctx := pipeClient(t, reader)
	done := make(chan error, 1)
	go func() { _, err := client.Plan(ctx); done <- err }()
	select {
	case <-reader.entered:
	case <-ctx.Done():
		t.Fatal("active request did not start")
	}
	short, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Inspect(short); !errors.Is(err, context.Canceled) {
		t.Fatal("queued request ignored cancellation")
	}
	close(reader.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := client.Inspect(ctx); err != nil {
		t.Fatal("queued cancellation closed a healthy connection")
	}
}

func TestCancellationInterruptsBlockedConnection(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// No reader accepts the hello request. Cancellation must interrupt Write.
	if _, err := Connect(ctx, left); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection did not cancel: %v", err)
	}
}

func TestServerRejectsMutationsPathsAndMalformedFrames(t *testing.T) {
	for _, tc := range []struct {
		name, input, expected string
		fails                 bool
	}{
		{"mutation", `{"version":1,"id":1,"method":"apply"}` + "\n", "unsupported_method", false},
		{"version", `{"version":999,"id":1,"method":"inspect"}` + "\n", "unsupported_protocol", false},
		{"path injection", `{"version":1,"id":1,"method":"inspect","root":"private-path"}` + "\n", "", true},
		{"missing id", `{"version":1,"method":"inspect"}` + "\n", "", true},
		{"trailing JSON", `{"version":1,"id":1,"method":"inspect"} {}` + "\n", "", true},
		{"truncated frame", `{"version":1,"id":1,"method":"inspect"}`, "", true},
		{"oversized", strings.Repeat("x", maxFrame+1) + "\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Serve(context.Background(), strings.NewReader(tc.input), &out, testReader{})
			if (err != nil) != tc.fails {
				t.Fatalf("unexpected serve result: %v", err)
			}
			if tc.expected != "" && !strings.Contains(out.String(), tc.expected) {
				t.Fatal("missing protocol rejection")
			}
			if strings.Contains(out.String(), "private-") || (err != nil && strings.Contains(err.Error(), "private-")) {
				t.Fatal("request contents leaked")
			}
		})
	}
}

func TestAdapterErrorNeverCrossesProtocol(t *testing.T) {
	var out bytes.Buffer
	err := Serve(context.Background(), strings.NewReader("{\"version\":1,\"id\":1,\"method\":\"inspect\"}\n"), &out, testReader{failed: true})
	if err != nil || !strings.Contains(out.String(), "inspection_failed") || strings.Contains(out.String(), "private-") {
		t.Fatal("adapter error leaked or disappeared")
	}
}

func TestClientRejectsIncompatibleAndUncorrelatedResponses(t *testing.T) {
	for _, body := range []string{
		`{"version":2,"id":2,"result":{}}` + "\n",
		`{"version":1,"id":99,"result":{}}` + "\n",
		`{"version":1,"id":2,"result":null}` + "\n",
		`{"version":1,"id":2,"result":{}}` + "\n",
		`{"version":1,"id":2,"error":"private-remote-error","result":{}}` + "\n",
		`{"version":1,"id":2,"error":"private-remote-error"}` + "\n",
		strings.Repeat("x", maxFrame+1) + "\n",
		`{"version":1,"id":2,"result":{"stack":"fixture"}}`,
	} {
		t.Run("response", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			left, right := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer right.Close()
				in := bufio.NewReader(right)
				_, _ = in.ReadString('\n')
				_ = writeFrame(right, response{Version: 1, ID: 1, Result: json.RawMessage(`{"version":1,"methods":["inspect","plan"],"read_only":true}`)})
				_, _ = in.ReadString('\n')
				_, _ = io.WriteString(right, body)
			}()
			client, err := Connect(ctx, left)
			if err != nil {
				left.Close()
				<-done
				t.Fatal(err)
			}
			_, err = client.Inspect(ctx)
			client.Close()
			<-done
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("invalid response accepted or echoed")
			}
		})
	}
}

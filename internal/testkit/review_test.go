package testkit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func reviewFixture(t *testing.T) (string, deployment.Engine, deployment.Review) {
	t.Helper()
	root := t.TempDir()
	if err := Seed(root, "private-before", "private-after"); err != nil {
		t.Fatal(err)
	}
	e := deployment.Engine{Root: root}
	r, err := e.Review(context.Background(), Stack{})
	if err != nil {
		t.Fatal(err)
	}
	return root, e, r
}

func effectCount(t *testing.T, root string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".fixture", "effects.log"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "configure-site\n")
}

func TestConfirmedReviewConvergesAndDuplicateApplyReturnsReceipt(t *testing.T) {
	root, e, review := reviewFixture(t)
	public, _ := json.Marshal(review)
	if !review.CanApply || strings.Contains(string(public), "private-") || strings.Contains(string(public), root) {
		t.Fatal("public review leaks private state or lacks fixture capability")
	}
	if _, err := e.ApplyReview(context.Background(), review.ID, false, Stack{}); err == nil {
		t.Fatal("unconfirmed review applied")
	}
	if effectCount(t, root) != 0 {
		t.Fatal("confirmation failure changed runtime")
	}
	status, err := e.ApplyReview(context.Background(), review.ID, true, Stack{})
	if err != nil || status.Status != "complete" || status.ReviewID != review.ID {
		t.Fatalf("apply failed: %v", err)
	}
	repeated, err := e.ApplyReview(context.Background(), review.ID, true, Stack{})
	if err != nil || repeated.ID != status.ID || effectCount(t, root) != 1 {
		t.Fatal("duplicate apply did not return the original receipt")
	}
}

func TestStaleConfigurationRuntimeIdentityAndExpiryStopBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"desired", "runtime-identity", "expired", "unknown-id", "different-root"} {
		t.Run(scenario, func(t *testing.T) {
			root, e, review := reviewFixture(t)
			id := review.ID
			switch scenario {
			case "desired":
				if err := deployment.WriteJSON(filepath.Join(root, ".fixture", "desired.json"), state{"changed"}); err != nil {
					t.Fatal(err)
				}
			case "runtime-identity":
				if err := deployment.WriteJSON(filepath.Join(root, ".fixture", "identity.json"), state{"replacement-runtime"}); err != nil {
					t.Fatal(err)
				}
			case "expired":
				path := filepath.Join(root, ".stack", "go-reviews", id+".json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var record map[string]any
				if json.Unmarshal(data, &record) != nil {
					t.Fatal("bad fixture")
				}
				public := record["review"].(map[string]any)
				public["created_at"] = time.Now().Add(-30 * time.Minute).UTC()
				public["expires_at"] = time.Now().Add(-15 * time.Minute).UTC()
				if err := deployment.WriteJSON(path, record); err != nil {
					t.Fatal(err)
				}
			case "unknown-id":
				id = strings.Repeat("f", 32)
			case "different-root":
				other := t.TempDir()
				if err := Seed(other, "private-before", "private-after"); err != nil {
					t.Fatal(err)
				}
				e.Root = other
			}
			if _, err := e.ApplyReview(context.Background(), id, true, Stack{}); err == nil {
				t.Fatal("stale or foreign review applied")
			}
			if effectCount(t, root) != 0 {
				t.Fatal("rejected review changed runtime")
			}
			status, err := e.Status()
			if err != nil || status.Status != "idle" {
				t.Fatal("review rejection registered a new operation")
			}
		})
	}
}

func TestFailedReviewedApplyNeedsExplicitResumeAndExactOperation(t *testing.T) {
	root, e, review := reviewFixture(t)
	status, err := e.ApplyReview(context.Background(), review.ID, true, Stack{Fault: AfterWrite})
	if err == nil || status.Status != "failed" {
		t.Fatal("expected failed checkpoint after persisted effect")
	}
	repeated, err := e.ApplyReview(context.Background(), review.ID, true, Stack{})
	if err != nil || repeated.Status != "failed" || repeated.ID != status.ID || effectCount(t, root) != 1 {
		t.Fatal("duplicate apply retried an unfinished action")
	}
	if _, err := e.ResumeReviewed(context.Background(), strings.Repeat("0", 32), Stack{}); err == nil {
		t.Fatal("wrong operation resumed")
	}
	if _, err := e.Review(context.Background(), Stack{}); err == nil {
		t.Fatal("new review ignored pending recovery")
	}
	status, err = e.ResumeReviewed(context.Background(), status.ID, Stack{})
	if err != nil || status.Status != "complete" || effectCount(t, root) != 1 {
		t.Fatal("explicit resume failed or duplicated effect")
	}
}

func TestStatusReadableDuringLockedApplyAndPreparationsAreBound(t *testing.T) {
	_, e, review := reviewFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := e.ApplyReview(context.Background(), review.ID, true, Stack{AfterPersist: func() { close(entered); <-release }})
		done <- err
	}()
	<-entered
	status, err := e.Status()
	if err != nil || status.Status != "running" || status.CurrentStep != "configure-site" {
		close(release)
		t.Fatal("status unavailable while deployment lock is held")
	}
	if _, err := e.ApplyReview(context.Background(), review.ID, true, Stack{}); err == nil {
		close(release)
		t.Fatal("concurrent apply ignored deployment lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUsedReviewCannotStartAgainAfterAnotherOperation(t *testing.T) {
	root, e, first := reviewFixture(t)
	if _, err := e.ApplyReview(context.Background(), first.ID, true, Stack{}); err != nil {
		t.Fatal(err)
	}
	if err := deployment.WriteJSON(filepath.Join(root, ".fixture", "desired.json"), state{"third"}); err != nil {
		t.Fatal(err)
	}
	second, err := e.Review(context.Background(), Stack{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyReview(context.Background(), second.ID, true, Stack{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyReview(context.Background(), first.ID, true, Stack{}); err == nil {
		t.Fatal("old consumed review reused after another operation")
	}
	if effectCount(t, root) != 2 {
		t.Fatal("old review caused an additional effect")
	}
}

func TestReservedReviewWithoutOperationCannotBeReplayed(t *testing.T) {
	root, e, review := reviewFixture(t)
	path := filepath.Join(root, ".stack", "go-reviews", review.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if json.Unmarshal(data, &record) != nil {
		t.Fatal("bad fixture")
	}
	record["used_by"] = strings.Repeat("a", 32)
	if err := deployment.WriteJSON(path, record); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyReview(context.Background(), review.ID, true, Stack{}); err == nil {
		t.Fatal("reserved review restarted without its operation record")
	}
	fresh, err := e.Review(context.Background(), Stack{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyReview(context.Background(), fresh.ID, true, Stack{}); err != nil {
		t.Fatal(err)
	}
	if effectCount(t, root) != 1 {
		t.Fatal("reservation recovery duplicated an effect")
	}
}

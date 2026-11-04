package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type accessTestReader struct {
	testReader
	calls int
	fail  bool
}

func (*accessTestReader) Methods() []string { return []string{"inspect", "plan", "runtime", "access"} }
func (r *accessTestReader) Access(context.Context) (deployment.AccessReport, error) {
	r.calls++
	if r.fail {
		return deployment.AccessReport{}, errors.New("private-adapter-password")
	}
	return deployment.AccessReport{Stack: "fixture", Links: []deployment.AccessValue{{Label: "Panel", Value: "https://example.test/private/"}}, Credentials: []deployment.AccessValue{{Label: "Password", Value: "private-password", Secret: true}}}, nil
}

func TestAccessProtocolIsExplicitAndSanitizesFailures(t *testing.T) {
	r := &accessTestReader{}
	c, ctx := pipeClient(t, r)
	if _, err := c.Inspect(ctx); err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 {
		t.Fatal("ordinary inspection fetched credentials")
	}
	a, err := c.Access(ctx)
	if err != nil || a.Credentials[0].Value != "private-password" || !a.Credentials[0].Secret {
		t.Fatal("explicit private access response failed")
	}
	// The correlated response above ensures Serve has finished reading fail.
	r.fail = true
	if _, err := c.Access(ctx); err == nil || strings.Contains(err.Error(), "private-adapter-password") {
		t.Fatal("adapter failure was accepted or leaked credentials")
	}
	old, oldctx := pipeClient(t, testReader{})
	if _, err := old.Access(oldctx); err == nil {
		t.Fatal("old controller unexpectedly supported access details")
	}
}

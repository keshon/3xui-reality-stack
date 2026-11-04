package console

import (
	"strings"
	"testing"
)

func TestInitializationInputIsBoundedAndNeverEchoed(t *testing.T) {
	for _, input := range []string{"", `{"private":"unterminated`, strings.Repeat(" ", 32<<10) + `{}`, string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})} {
		if _, err := ReadInitialization(strings.NewReader(input)); err == nil || strings.Contains(err.Error(), "unterminated") {
			t.Fatal("invalid initialization input accepted or echoed")
		}
	}
	if _, err := ReadInitialization(nil); err == nil {
		t.Fatal("missing initialization input accepted")
	}
	if data, err := ReadInitialization(strings.NewReader("{\"schema_version\":1}\n")); err != nil || string(data) != "{\"schema_version\":1}\n" {
		t.Fatal("valid initialization JSON changed")
	}
}

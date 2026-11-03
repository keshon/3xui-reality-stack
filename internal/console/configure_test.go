package console

import (
	"strings"
	"testing"
)

func TestPasswordStdinPreservesLiteralsAndBoundsInput(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		literal := `  $HOME '"\ пароль & <>  `
		got, err := ReadPassword(strings.NewReader(literal + suffix))
		if err != nil || got != literal {
			t.Fatal("password stdin changed a literal")
		}
	}
	for _, input := range []string{"", "\n", "private\nvalue", "private\r", "private\u0080", "private\u2028value", string([]byte{0xff}), strings.Repeat("x", 4097)} {
		_, err := ReadPassword(strings.NewReader(input))
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid password input accepted or echoed")
		}
	}
	if _, err := ReadPassword(nil); err == nil {
		t.Fatal("missing password input accepted")
	}
	if got, err := ReadPassword(strings.NewReader(strings.Repeat("x", 4096) + "\r\n")); err != nil || len(got) != 4096 {
		t.Fatal("maximum password length was not accepted")
	}
}

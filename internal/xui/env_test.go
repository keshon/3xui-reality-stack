package xui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiteralPasswordRoundtripAndLinePreservation(t *testing.T) {
	for _, value := range []string{newPassword, "both ends ", " leading", `C:\path\`, "# comment = value", "пароль", `${USER} $$ \" '`} {
		before := []byte("# keep\r\nexport PANEL_PASS='old' # previous\r\nOTHER=literal\r\n")
		data, err := replacePassword(before, "old", value)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte("# keep\r\n")) || !bytes.HasSuffix(data, []byte("OTHER=literal\r\n")) {
			t.Fatal("other lines changed")
		}
		line := strings.Split(string(data), "\n")[1]
		_, encoded, _ := strings.Cut(line, "=")
		got, err := parseLiteral(strings.TrimSpace(encoded))
		if err != nil || got != value {
			t.Fatal("literal password did not roundtrip")
		}
	}
}

func TestPasswordOutputAcceptedByPythonLiteralParser(t *testing.T) {
	interpreter := os.Getenv("STACK_TEST_PYTHON")
	if interpreter == "" {
		for _, name := range []string{"python3", "python"} {
			if path, err := exec.LookPath(name); err == nil {
				interpreter = path
				break
			}
		}
	}
	if interpreter == "" {
		t.Skip("Python unavailable for literal dotenv interoperability test")
	}
	for _, value := range []string{newPassword, `C:\path\`, "# comment = value", " both ends "} {
		data, err := replacePassword([]byte("PANEL_PASS='old'\n"), "old", value)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(interpreter, "-c", "import json,sys; from stackctl.envio import parse_env; print(json.dumps(parse_env(sys.stdin.read())))")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = append(os.Environ(), "PYTHONUTF8=1")
		cmd.Stdin = bytes.NewReader(data)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal("Python rejected generated literal dotenv")
		}
		var parsed map[string]string
		if json.Unmarshal(out, &parsed) != nil || parsed["PANEL_PASS"] != value {
			t.Fatal("Go/Python dotenv contract differs")
		}
	}
}

func TestInvalidPasswordInputsRejected(t *testing.T) {
	for _, text := range []string{"PANEL_PASS=old\nPANEL_PASS=old\n", "OTHER=old\n", "PANEL_PASS=wrong\n", "PANEL_PASS='unclosed\n"} {
		if _, err := replacePassword([]byte(text), "old", "desired"); err == nil {
			t.Fatal("unsafe password source accepted")
		}
	}
	for _, desired := range []string{"", "a\nb", "a\x00b", "a\x7fb", "\u0085", "\u2028", "\u2029"} {
		if _, err := replacePassword([]byte("PANEL_PASS=old\n"), "old", desired); err == nil {
			t.Fatal("invalid desired password accepted")
		}
	}
}

func TestPasswordAtStartOfBOMFile(t *testing.T) {
	data, err := replacePassword([]byte("\ufeffPANEL_PASS=old\r\n"), "old", "new")
	if err != nil || string(data) != "\ufeffPANEL_PASS=\"new\"\r\n" {
		t.Fatal("BOM password file was not preserved")
	}
}

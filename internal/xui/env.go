package xui

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func controls(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

// replacePassword preserves other lines byte-for-byte and follows the Python
// literal dotenv parser. It never sources shell text or expands dollar signs.
func replacePassword(data []byte, old, desired string) ([]byte, error) {
	if controls(desired) || controls(old) || desired == "" {
		return nil, errors.New("invalid literal password")
	}
	prefix := ""
	text := string(data)
	if strings.HasPrefix(text, "\ufeff") {
		prefix, text = "\ufeff", strings.TrimPrefix(text, "\ufeff")
	}
	lines := strings.Split(text, "\n")
	found := false
	for n, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, has := strings.Cut(line, "=")
		if !has || strings.TrimSpace(key) != "PANEL_PASS" {
			continue
		}
		if found {
			return nil, errors.New("duplicate password entry in generated environment")
		}
		literal, err := parseLiteral(strings.TrimSpace(value))
		if err != nil || literal != old {
			return nil, errors.New("generated password differs from applied configuration")
		}
		quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(desired)
		lines[n] = `PANEL_PASS="` + quoted + `"`
		if strings.HasSuffix(raw, "\r") {
			lines[n] += "\r"
		}
		found = true
	}
	if !found {
		return nil, errors.New("generated password entry is missing")
	}
	return []byte(prefix + strings.Join(lines, "\n")), nil
}

func parseLiteral(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] != '\'' && value[0] != '"' {
		literal, _, _ := strings.Cut(value, " #")
		literal = strings.TrimRight(literal, " \t")
		if controls(literal) {
			return "", errors.New("invalid literal")
		}
		return literal, nil
	}
	quote := value[0]
	var b strings.Builder
	for n := 1; n < len(value); n++ {
		ch := value[n]
		if ch == quote {
			tail := strings.TrimSpace(value[n+1:])
			if (tail != "" && !strings.HasPrefix(tail, "#")) || controls(b.String()) {
				return "", errors.New("invalid literal")
			}
			return b.String(), nil
		}
		if ch == '\\' && n+1 < len(value) {
			next := value[n+1]
			if quote == '\'' && next == '\'' {
				b.WriteByte(next)
				n++
				continue
			}
			if quote == '"' {
				escapes := map[byte]byte{'"': '"', '\\': '\\', '$': '$', 'n': '\n', 'r': '\r', 't': '\t'}
				if decoded, ok := escapes[next]; ok {
					b.WriteByte(decoded)
					n++
					continue
				}
			}
		}
		b.WriteByte(ch)
	}
	return "", errors.New("invalid literal")
}

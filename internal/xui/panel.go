package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"time"
)

var errCredentials = errors.New("panel authentication rejected")

// Panel speaks only the pinned v3.7.0 API. It never logs service responses,
// credentials or URLs. Plain HTTP is restricted to loopback development servers.
type Panel struct {
	base      string
	transport *http.Transport
}
type panelSession struct {
	base string
	http *http.Client
}

func NewPanel(base string) (*Panel, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("invalid panel endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("panel endpoint requires HTTPS")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Panel{base: strings.TrimRight(u.String(), "/"), transport: transport}, nil
}

func (p *Panel) login(ctx context.Context, username, password string) (*panelSession, error) {
	jar, _ := cookiejar.New(nil)
	s := &panelSession{base: p.base, http: &http.Client{Jar: jar, Transport: p.transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	// Each authentication is deliberately a new cookie jar, proving the supplied
	// password works rather than reusing a session that survived a rotation.
	var twoFactor bool
	if err := s.json(ctx, "/getTwoFactorEnable", map[string]string{}, &twoFactor); err != nil {
		return nil, err
	}
	if twoFactor {
		return nil, errors.New("two-factor authentication needs an explicit supported workflow")
	}
	if err := s.json(ctx, "/login", map[string]string{"username": username, "password": password}, nil); err != nil {
		if errors.Is(err, errRejected) {
			return nil, errCredentials
		}
		return nil, err
	}
	return s, nil
}

var errRejected = errors.New("panel request rejected")

func (s *panelSession) request(ctx context.Context, path string, body any, limit int64) ([]byte, error) {
	var reader io.Reader
	method := http.MethodGet
	csrf := ""
	if body != nil {
		var token string
		if err := s.json(ctx, "/csrf-token", nil, &token); err != nil {
			return nil, err
		}
		if token == "" {
			return nil, errors.New("panel CSRF token is missing")
		}
		csrf = token
		data, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("cannot encode panel request")
		}
		reader, method = bytes.NewReader(data), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, reader)
	if err != nil {
		return nil, errors.New("cannot build panel request")
	}
	u, _ := url.Parse(s.base)
	req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	req.Header.Set("Referer", s.base+"/")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-TOKEN", csrf)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, errors.New("panel request failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("panel returned an unexpected HTTP status")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("panel response is incomplete or too large")
	}
	return data, nil
}

func (s *panelSession) json(ctx context.Context, path string, body, result any) error {
	data, err := s.request(ctx, path, body, 32<<20)
	if err != nil {
		return err
	}
	var reply struct {
		Success bool            `json:"success"`
		Object  json.RawMessage `json:"obj"`
	}
	if json.Unmarshal(data, &reply) != nil {
		return errors.New("invalid panel JSON response")
	}
	if !reply.Success {
		return errRejected
	}
	if result != nil && (len(reply.Object) == 0 || bytes.Equal(reply.Object, []byte("null")) || json.Unmarshal(reply.Object, result) != nil) {
		return errors.New("invalid panel response object")
	}
	return nil
}

func (s *panelSession) changePassword(ctx context.Context, username, old, desired string) error {
	return s.changeCredentials(ctx, username, old, username, desired)
}

func (s *panelSession) changeCredentials(ctx context.Context, username, old, desiredUser, desiredPassword string) error {
	return s.json(ctx, "/panel/api/setting/updateUser", map[string]string{"oldUsername": username, "oldPassword": old, "newUsername": desiredUser, "newPassword": desiredPassword}, nil)
}

// preservation captures all exported clients/attachments and the settings view.
// Counters and online state are excluded by the upstream export endpoint.
func (s *panelSession) preservation(ctx context.Context) ([]byte, error) {
	var rows []json.RawMessage
	if err := s.json(ctx, "/panel/api/clients/export", nil, &rows); err != nil {
		return nil, err
	}
	canonical := make([]string, len(rows))
	for n, row := range rows {
		var item map[string]json.RawMessage
		if json.Unmarshal(row, &item) != nil || item["client"] == nil || item["inboundIds"] == nil {
			return nil, errors.New("unsupported client export")
		}
		var client map[string]json.RawMessage
		if json.Unmarshal(item["client"], &client) != nil || client == nil {
			return nil, errors.New("unsupported exported client")
		}
		for _, key := range []string{"email", "id", "password", "subId"} {
			if client[key] == nil {
				return nil, errors.New("incomplete exported identity")
			}
		}
		data, err := canonicalJSON(row)
		if err != nil {
			return nil, err
		}
		canonical[n] = string(data)
	}
	slices.Sort(canonical)
	var settings json.RawMessage
	if err := s.json(ctx, "/panel/api/setting/all", map[string]string{}, &settings); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(settings, &object) != nil || object == nil {
		return nil, errors.New("unsupported settings view")
	}
	for _, key := range []string{"webBasePath", "subPath", "subJsonPath", "subURI"} {
		if object[key] == nil {
			return nil, errors.New("incomplete subscription settings")
		}
	}
	data, err := canonicalJSON(settings)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Clients  []string        `json:"clients"`
		Settings json.RawMessage `json:"settings"`
	}{canonical, data})
}

func canonicalJSON(data []byte) ([]byte, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil, errors.New("invalid preservation data")
	}
	return json.Marshal(value)
}

func (s *panelSession) database(ctx context.Context) ([]byte, error) {
	data, err := s.request(ctx, "/panel/api/server/getDb", nil, 256<<20)
	if err != nil {
		return nil, err
	}
	// The pinned endpoint uses SQLite's backup API. This validates its format,
	// not SQLite integrity; a container-level restore test is a separate gate.
	if len(data) < 100 || !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return nil, errors.New("panel did not return a SQLite recovery snapshot")
	}
	return data, nil
}

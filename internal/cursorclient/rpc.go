package cursorclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	agentService = "/agent.v1.AgentService/"
	bidiService  = "/aiserver.v1.BidiService/"
)

// Client calls Cursor's Connect agent service over HTTP/1 RunSSE plus BidiAppend.
type Client struct {
	HTTP          *http.Client
	API           string
	AccessToken   string
	TeamID        string
	LocalCLI      bool
	ClientVersion string
	ClientType    string
}

// Model is one entry from GetUsableModels.
type Model struct {
	ID          string
	DisplayName string
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return cursorHTTP1()
}

// cursorHTTP1 speaks the HTTP/1 RunSSE transport. The default Go client would
// negotiate HTTP/2, and Cursor uses a different bidirectional method there.
func cursorHTTP1() *http.Client {
	cursorHTTPOnce.Do(func() {
		base, _ := http.DefaultTransport.(*http.Transport)
		var transport *http.Transport
		if base != nil {
			transport = base.Clone()
		} else {
			transport = &http.Transport{}
		}
		transport.DisableCompression = true
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		cursorHTTP = &http.Client{Transport: transport}
	})
	return cursorHTTP
}

var (
	cursorHTTPOnce sync.Once
	cursorHTTP     *http.Client
)

func (c Client) api() string {
	if strings.TrimSpace(c.API) == "" {
		return defaultAPI
	}
	return strings.TrimRight(c.API, "/")
}

func (c Client) Models(ctx context.Context) ([]Model, error) {
	reqMsg, err := New("GetUsableModelsRequest")
	if err != nil {
		return nil, err
	}
	body, err := reqMsg.Encode()
	if err != nil {
		return nil, err
	}
	raw, err := c.unary(ctx, "GetUsableModels", body)
	if err != nil {
		return nil, err
	}
	msg, err := Decode("GetUsableModelsResponse", raw)
	if err != nil {
		return nil, err
	}
	var out []Model
	for _, item := range msg.Messages("models") {
		id := item.String("model_id")
		if id == "" {
			continue
		}
		name := item.String("display_name")
		if name == "" {
			name = item.String("display_name_short")
		}
		out = append(out, Model{ID: id, DisplayName: name})
	}
	return out, nil
}

func (c Client) unary(ctx context.Context, method string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api()+agentService+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.headers(req, "application/proto")
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	resp, err := io.ReadAll(io.LimitReader(res.Body, maxMessageBytes))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("cursor %s returned status %d", method, res.StatusCode)
	}
	return resp, nil
}

type serverStream struct {
	body io.ReadCloser
	br   *bufio.Reader
}

func (c Client) openRun(ctx context.Context, requestID string) (*serverStream, error) {
	id, err := New("BidiRequestId")
	if err != nil {
		return nil, err
	}
	id.Set("request_id", requestID)
	payload, err := id.Encode()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeEnvelope(&buf, 0, payload); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api()+agentService+"RunSSE", &buf)
	if err != nil {
		return nil, err
	}
	c.headers(req, "application/connect+proto")
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("cursor RunSSE returned status %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return &serverStream{body: res.Body, br: bufio.NewReader(res.Body)}, nil
}

func (s *serverStream) Next() (*Value, error) {
	for {
		flags, payload, err := readEnvelope(s.br)
		if err != nil {
			return nil, err
		}
		if flags&0x01 != 0 {
			return nil, errors.New("compressed cursor frame is not supported")
		}
		if flags&0x02 != 0 {
			if err := endStreamError(payload); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		msg, err := Decode("AgentServerMessage", payload)
		if err != nil {
			return nil, err
		}
		return msg, nil
	}
}

func (s *serverStream) Close() error { return s.body.Close() }

func (c Client) append(ctx context.Context, requestID string, seq int64, msg *Value) error {
	payload, err := msg.Encode()
	if err != nil {
		return err
	}
	reqMsg, err := New("BidiAppendRequest")
	if err != nil {
		return err
	}
	id, err := New("BidiRequestId")
	if err != nil {
		return err
	}
	id.Set("request_id", requestID)
	reqMsg.Set("data", hex.EncodeToString(payload))
	reqMsg.Set("request_id", id)
	reqMsg.Set("append_seqno", seq)
	body, err := reqMsg.Encode()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api()+bidiService+"BidiAppend", bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.headers(req, "application/proto")
	res, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("cursor BidiAppend returned status %d", res.StatusCode)
	}
	if seq < 0 {
		return errors.New("cursor append sequence went backwards")
	}
	return nil
}

func (c Client) headers(req *http.Request, contentType string) {
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("x-cursor-client-version", c.clientVersion())
	req.Header.Set("x-cursor-client-type", c.clientType())
	req.Header.Set("x-ghost-mode", "true")
	if req.Header.Get("x-request-id") == "" {
		if id, err := newUUID(); err == nil {
			req.Header.Set("x-request-id", id)
		}
	}
	if token := strings.TrimSpace(c.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if team := strings.TrimSpace(c.TeamID); team != "" {
		req.Header.Set("x-cursor-team-id", team)
	}
	if c.LocalCLI {
		req.Header.Set("local-cli-mode", "true")
	}
}

func (c Client) clientVersion() string {
	if v := strings.TrimSpace(c.ClientVersion); v != "" {
		return normalizeClientVersion(v)
	}
	if v := strings.TrimSpace(os.Getenv("CURSOR_CLIENT_VERSION")); v != "" {
		return normalizeClientVersion(v)
	}
	// The agent host uses a date-stamped CLI version when no build id is baked in.
	return "cli-" + time.Now().UTC().Format("2006.01.02")
}

func (c Client) clientType() string {
	if v := strings.TrimSpace(c.ClientType); v != "" {
		return v
	}
	return "cli"
}

func normalizeClientVersion(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToLower(v), "cli") {
		return v
	}
	return "cli-" + v
}

func writeEnvelope(w io.Writer, flags byte, payload []byte) error {
	if len(payload) > maxMessageBytes {
		return errors.New("cursor frame exceeds size limit")
	}
	var head [5]byte
	head[0] = flags
	binary.BigEndian.PutUint32(head[1:], uint32(len(payload)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readEnvelope(r io.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > maxMessageBytes {
		return 0, nil, errors.New("cursor frame exceeds size limit")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return head[0], payload, nil
}

func endStreamError(payload []byte) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	var body struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details []connectDetail `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &body) != nil || body.Error.Message == "" && body.Error.Code == "" {
		return nil
	}
	detail := describeConnectDetails(body.Error.Details)
	message := strings.TrimSpace(body.Error.Message)
	if detail != "" && (message == "" || strings.EqualFold(message, "error")) {
		message = detail
	} else if detail != "" && !strings.Contains(message, detail) {
		message = message + ": " + detail
	}
	if body.Error.Code != "" {
		return fmt.Errorf("cursor stream ended: %s: %s", body.Error.Code, message)
	}
	return fmt.Errorf("cursor stream ended: %s", message)
}

type connectDetail struct {
	Type  string          `json:"type"`
	Value string          `json:"value"`
	Debug json.RawMessage `json:"debug"`
}

func describeConnectDetails(details []connectDetail) string {
	var parts []string
	for _, detail := range details {
		if text := describeDebugDetail(detail.Debug); text != "" {
			parts = append(parts, text)
			continue
		}
		if text := describeBinaryDetail(detail.Value); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "; ")
}

func describeDebugDetail(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return ""
	}
	var body struct {
		Error   string `json:"error"`
		Details struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	return joinErrorText(cursorErrorName(body.Error), body.Details.Title, body.Details.Detail)
}

func describeBinaryDetail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil {
		return ""
	}
	code, title, detail := decodeErrorDetails(raw)
	name := ""
	if code > 0 {
		name = cursorErrorNameByCode(code)
	}
	return joinErrorText(name, title, detail)
}

func joinErrorText(parts ...string) string {
	var out []string
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return strings.Join(out, ": ")
}

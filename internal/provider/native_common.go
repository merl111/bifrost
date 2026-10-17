package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var errNativeSSEEOF = errors.New("provider: streaming response ended before terminal event")

type NativeConfig struct {
	BaseURL          string
	Model            string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
}

type HTTPError struct {
	StatusCode int
	RetryAfter string
}

func (e *HTTPError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("provider: server returned HTTP %d (retry-after %s)", e.StatusCode, e.RetryAfter)
	}
	return fmt.Sprintf("provider: server returned HTTP %d", e.StatusCode)
}

func nativeEndpoint(raw, suffix string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("provider: base URL must include scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("provider: credentials, queries, and fragments are not allowed in base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return "", errors.New("provider: HTTPS is required except for loopback HTTP")
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, suffix) {
		path += suffix
	}
	u.Path, u.RawQuery, u.Fragment = path, "", ""
	return u.String(), nil
}

func nativeHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func nativeMaxBody(n int) int {
	if n <= 0 {
		return defaultResponseMax
	}
	return n
}

func readNativeBody(body io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("provider: read response: %w", err)
	}
	if len(b) > limit {
		return nil, errors.New("provider: response exceeds configured limit")
	}
	return b, nil
}

func nativeRequest(ctx context.Context, client clientDoer, endpoint string, body any, key string, headers map[string]string) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("provider: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("provider: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if key != "" {
		// The native adapters use different header names; callers pass the
		// complete auth header in headers when they need one.
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, &HTTPError{StatusCode: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	return resp, nil
}

// nativeSSE reads SSE data frames and stops as soon as stop returns true. It
// deliberately bounds each line and the complete body, so a server cannot
// make a single ReadString allocation unbounded.
func nativeSSE(ctx context.Context, body io.Reader, limit int, onFrame func(string) (bool, error)) error {
	r := bufio.NewReaderSize(io.LimitReader(body, int64(limit)+1), 32<<10)
	var total int
	var data []string
	for {
		line, err := r.ReadString('\n')
		total += len(line)
		if total > limit {
			return errors.New("provider: streaming response exceeds configured limit")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if len(data) > 0 {
				stop, dispatchErr := onFrame(strings.Join(data, "\n"))
				data = nil
				if dispatchErr != nil {
					return dispatchErr
				}
				if stop {
					return nil
				}
			}
		} else if !strings.HasPrefix(line, ":") {
			field, value, found := strings.Cut(line, ":")
			if found && strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			if field == "data" {
				data = append(data, value)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("provider: read SSE response: %w", err)
			}
			if len(data) > 0 {
				stop, dispatchErr := onFrame(strings.Join(data, "\n"))
				if dispatchErr != nil {
					return dispatchErr
				}
				if stop {
					return nil
				}
			}
			return errNativeSSEEOF
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func imageDataURL(image Image) (string, error) {
	if (len(image.Data) == 0) == (strings.TrimSpace(image.URL) == "") {
		return "", errors.New("provider: image must specify exactly one of data or URL")
	}
	if image.URL != "" {
		return image.URL, nil
	}
	mime := image.MediaType
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image.Data), nil
}

func validateImage(image Image) error {
	if (len(image.Data) == 0) == (strings.TrimSpace(image.URL) == "") {
		return errors.New("provider: image must specify exactly one of data or URL")
	}
	return nil
}

func validateObject(raw json.RawMessage, what string) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("provider: " + what + " is empty")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return errors.New("provider: " + what + " must be a JSON object")
	}
	return nil
}

func copyRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

package cursorclient

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCursorRequestsUseHTTP1AndCLIIdentity(t *testing.T) {
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := Client{API: srv.URL, AccessToken: "tok", ClientVersion: "2026.10.05-test"}
	if _, err := c.unary(context.Background(), "GetUsableModels", []byte{0}); err != nil {
		t.Fatal(err)
	}
	if header.Get("x-cursor-client-version") != "cli-2026.10.05-test" {
		t.Fatalf("version = %q", header.Get("x-cursor-client-version"))
	}
	if header.Get("x-cursor-client-type") != "cli" || header.Get("x-ghost-mode") != "true" {
		t.Fatalf("identity headers = %v", header)
	}
	if header.Get("Authorization") != "Bearer tok" || header.Get("Connect-Protocol-Version") != "1" {
		t.Fatalf("auth headers = %v", header)
	}
	if header.Get("x-request-id") == "" {
		t.Fatal("missing request id")
	}
	transport, ok := c.httpClient().Transport.(*http.Transport)
	if !ok || transport.TLSNextProto == nil || len(transport.TLSNextProto) != 0 || transport.ForceAttemptHTTP2 {
		t.Fatal("default cursor client still negotiates HTTP/2")
	}
}

func TestStreamErrorIncludesCursorDetail(t *testing.T) {
	custom := appendStringField(nil, 1, "Update Cursor")
	custom = appendStringField(custom, 2, "This client is no longer accepted")
	var raw []byte
	raw = append(raw, 0x08, 30)
	raw = append(raw, 0x12, byte(len(custom)))
	raw = append(raw, custom...)
	payload := []byte(`{"error":{"code":"resource_exhausted","message":"Error","details":[{"type":"aiserver.v1.ErrorDetails","value":"` + base64.StdEncoding.EncodeToString(raw) + `"}]}}`)
	err := endStreamError(payload)
	if err == nil || !strings.Contains(err.Error(), "OUTDATED_CLIENT") || !strings.Contains(err.Error(), "Update Cursor") {
		t.Fatalf("error = %v", err)
	}

	debug := []byte(`{"error":{"code":"resource_exhausted","message":"Error","details":[{"debug":{"error":"PRO_USER_RATE_LIMIT_EXCEEDED","details":{"title":"You've hit your usage limit","detail":"Enable usage-based pricing"}}}]}}`)
	err = endStreamError(debug)
	if err == nil || !strings.Contains(err.Error(), "PRO_USER_RATE_LIMIT_EXCEEDED") || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("debug error = %v", err)
	}
}

func appendStringField(dst []byte, field int, value string) []byte {
	dst = append(dst, byte(field<<3|2), byte(len(value)))
	return append(dst, value...)
}

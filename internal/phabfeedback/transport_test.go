package phabfeedback

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPTransportPropagatesContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "expected")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if value := request.Context().Value(contextKey{}); value != "expected" {
			t.Fatalf("unexpected request context value: %v", value)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})}
	body, err := (httpTransport{client: client}).Request(ctx, http.MethodGet, "https://phab.example", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected response: %q", body)
	}
}

func TestConduitErrorsRedactToken(t *testing.T) {
	transport := &fakeTransport{responses: []any{response(map[string]any{
		"result": nil, "error_code": "ERR-FAIL", "error_info": "very-secret-token was rejected",
	})}}
	client := conduitClient{host: "https://phab.example", token: "very-secret-token", transport: transport}
	var result any
	err := client.call("transaction.search", map[string]any{}, &result)
	if err == nil || strings.Contains(err.Error(), "very-secret-token") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestConduitPreservesLargeNumbers(t *testing.T) {
	transport := &fakeTransport{responses: []any{response(map[string]any{
		"result": map[string]any{
			"data":   []map[string]any{{"id": 7654321}},
			"cursor": map[string]any{"after": nil},
		},
		"error_code": nil,
		"error_info": nil,
	})}}
	client := conduitClient{host: "https://phab.example", token: "api-token", transport: transport}
	result, err := client.search("transaction.search", map[string]any{}, nil, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := stringValue(result.Data[0]["id"]); got != "7654321" {
		t.Fatalf("large Conduit number = %q, want 7654321", got)
	}
}

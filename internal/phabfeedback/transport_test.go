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
	response, err := (httpTransport{client: client}).Request(ctx, http.MethodGet, "https://phab.example", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != "ok" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestHTMLResponseDetails(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantTitle   string
		wantHTML    bool
	}{
		{
			name:        "content type",
			contentType: "text/html; charset=utf-8",
			body:        `<html><title>Making sure you&#39;re not a bot!</title></html>`,
			wantTitle:   "Making sure you're not a bot!",
			wantHTML:    true,
		},
		{
			name:      "body prefix",
			body:      " \n<!doctype html><title> Challenge \n page </title>",
			wantTitle: "Challenge page",
			wantHTML:  true,
		},
		{
			name:        "json",
			contentType: "application/json",
			body:        `{"result":{}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			title, isHTML := htmlResponseDetails(transportResponse{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {test.contentType}},
				Body:       []byte(test.body),
			})
			if title != test.wantTitle || isHTML != test.wantHTML {
				t.Fatalf("html details = %q, %t; want %q, %t", title, isHTML, test.wantTitle, test.wantHTML)
			}
		})
	}
}

func TestHTTPTransportAddsDefaultAcceptHeader(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Accept"); got != "*/*" {
			t.Fatalf("Accept = %q, want */*", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"result":{}}`)),
		}, nil
	})}
	_, err := (httpTransport{client: client}).Request(
		t.Context(),
		http.MethodPost,
		"https://we.phorge.it/api/user.whoami",
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		strings.NewReader("params={}"),
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTransportPreservesExplicitAcceptHeader(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q, want application/json", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"result":{}}`)),
		}, nil
	})}
	_, err := (httpTransport{client: client}).Request(
		t.Context(),
		http.MethodGet,
		"https://phab.example/api/test",
		http.Header{"Accept": {"application/json"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTransportReportsHTMLTitleForErrorStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Body: io.NopCloser(strings.NewReader(
				`<html><title>Making sure you're not a bot!</title></html>`,
			)),
		}, nil
	})}
	_, err := (httpTransport{client: client}).Request(
		t.Context(),
		http.MethodGet,
		"https://phab.example/api/test",
		nil,
		nil,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "HTTP 503") ||
		!strings.Contains(err.Error(), "Making sure you're not a bot!") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConduitReportsHTMLChallenge(t *testing.T) {
	requests := transportFunc(func(
		string,
		string,
		http.Header,
		io.Reader,
	) (transportResponse, error) {
		return transportResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Body:       []byte(`<html><title>Making sure you're not a bot!</title></html>`),
		}, nil
	})
	client := conduitClient{host: "https://we.phorge.it", token: "token", transport: requests}
	var result any
	err := client.call("user.whoami", map[string]any{}, &result)
	if err == nil ||
		!strings.Contains(err.Error(), "HTTP 200") ||
		!strings.Contains(err.Error(), "Making sure you're not a bot!") ||
		!strings.Contains(err.Error(), "bot-protection challenge") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConduitKeepsInvalidJSONErrorForNonHTMLResponse(t *testing.T) {
	requests := transportFunc(func(
		string,
		string,
		http.Header,
		io.Reader,
	) (transportResponse, error) {
		return transportResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       []byte("not json"),
		}, nil
	})
	client := conduitClient{host: "https://phab.example", token: "token", transport: requests}
	var result any
	err := client.call("user.whoami", map[string]any{}, &result)
	if err == nil ||
		!strings.Contains(err.Error(), "invalid JSON response") ||
		strings.Contains(err.Error(), "bot-protection") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebCSRFErrorIncludesHTMLTitle(t *testing.T) {
	requests := transportFunc(func(
		string,
		string,
		http.Header,
		io.Reader,
	) (transportResponse, error) {
		return transportResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Body:       []byte(`<html><title>Making sure you're not a bot!</title></html>`),
		}, nil
	})
	client := webClient{host: "https://phab.example", cookie: "phsid=cookie", transport: requests}
	_, err := client.csrf()
	if err == nil || !strings.Contains(err.Error(), `page title: "Making sure you're not a bot!"`) {
		t.Fatalf("unexpected error: %v", err)
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

package phabfeedback

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func useLocalHTTP2Transport(t *testing.T, handler http.Handler) *http.Transport {
	t.Helper()
	t.Setenv("GODEBUG", "http2client=1")
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	original := http.DefaultTransport
	defaultTransport, ok := original.(*http.Transport)
	if !ok {
		t.Fatal("local TLS tests require the standard HTTP transport")
	}
	base := defaultTransport.Clone()
	base.Proxy = nil
	base.TLSClientConfig = &tls.Config{
		RootCAs: roots,
		// Verify the local server's certificate while routing production hostnames to it.
		ServerName: "127.0.0.1",
	}
	base.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	base.Protocols = new(http.Protocols)
	base.Protocols.SetHTTP1(true)
	base.Protocols.SetHTTP2(true)
	http.DefaultTransport = base
	t.Cleanup(func() {
		http.DefaultTransport = original
		base.CloseIdleConnections()
	})
	return base
}

func TestDefaultHTTPClientNegotiatedProtocol(t *testing.T) {
	base := useLocalHTTP2Transport(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Request-Protocol", request.Proto)
		_, _ = io.Copy(io.Discard, request.Body)
		_, _ = io.WriteString(writer, "ok")
	}))
	tests := []struct {
		name, host, wantProtocol string
	}{
		{"mozilla", "https://phabricator.services.mozilla.com", "HTTP/1.1"},
		{"uppercase", "https://PHABRICATOR.SERVICES.MOZILLA.COM", "HTTP/1.1"},
		{"port and path", "https://phabricator.services.mozilla.com:8443/phab", "HTTP/1.1"},
		{"other host", "https://phab.example", "HTTP/2.0"},
		{"suffix lookalike", "https://phabricator.services.mozilla.com.example", "HTTP/2.0"},
		{"subdomain", "https://other.phabricator.services.mozilla.com", "HTTP/2.0"},
		{"prefix lookalike", "https://notphabricator.services.mozilla.com", "HTTP/2.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := defaultHTTPClient(test.host)
			t.Cleanup(client.CloseIdleConnections)
			if client.Timeout != 60*time.Second {
				t.Fatalf("timeout = %v, want 60s", client.Timeout)
			}
			if test.wantProtocol == "HTTP/1.1" {
				requests, ok := client.Transport.(*http.Transport)
				if !ok || requests == base {
					t.Fatal("Mozilla client must use a cloned transport")
				}
				if requests.Proxy != nil || requests.TLSClientConfig.RootCAs != base.TLSClientConfig.RootCAs ||
					requests.TLSClientConfig.InsecureSkipVerify || requests.DialContext == nil ||
					requests.IdleConnTimeout != base.IdleConnTimeout ||
					requests.TLSHandshakeTimeout != base.TLSHandshakeTimeout {
					t.Fatal("Mozilla transport did not preserve the base transport settings")
				}
			} else if client.Transport != nil {
				t.Fatal("unaffected client must retain the default transport")
			}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				response, err := (httpTransport{client: client}).Request(
					t.Context(), method, test.host+"/api/test", nil, strings.NewReader("params={}"),
				)
				if err != nil {
					t.Fatal(err)
				}
				if got := response.Header.Get("X-Request-Protocol"); got != test.wantProtocol {
					t.Fatalf("%s protocol = %q, want %q", method, got, test.wantProtocol)
				}
			}
		})
	}
	if http.DefaultTransport != base || !base.Protocols.HTTP1() || !base.Protocols.HTTP2() ||
		!slices.Contains(base.TLSClientConfig.NextProtos, "h2") {
		t.Fatal("client construction changed the global transport")
	}
}

func TestDefaultHTTPClientPreservesCustomDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	custom := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})
	http.DefaultTransport = custom
	t.Cleanup(func() { http.DefaultTransport = original })
	client := defaultHTTPClient("https://phabricator.services.mozilla.com")
	if client.Transport != nil {
		t.Fatal("client replaced the custom default transport")
	}
	if _, err := (httpTransport{client: client}).Request(
		t.Context(), http.MethodGet, "https://phabricator.services.mozilla.com/", nil, nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestMozillaHTTPTransportPreservesRateLimitErrors(t *testing.T) {
	for _, retryAfter := range []string{"", "60"} {
		t.Run("Retry-After="+retryAfter, func(t *testing.T) {
			var requests atomic.Int32
			useLocalHTTP2Transport(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.ProtoMajor != 1 {
					t.Errorf("protocol = %s, want HTTP/1.1", request.Proto)
				}
				if retryAfter != "" {
					writer.Header().Set("Retry-After", retryAfter)
				}
				http.Error(writer, "rate limited", http.StatusTooManyRequests)
			}))
			client := defaultHTTPClient("https://phabricator.services.mozilla.com")
			t.Cleanup(client.CloseIdleConnections)
			_, err := (httpTransport{client: client}).Request(
				t.Context(), http.MethodPost, "https://phabricator.services.mozilla.com/api/test",
				nil, strings.NewReader("params={}"),
			)
			if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
				t.Fatalf("rate-limit error = %v, want HTTP 429", err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("request count = %d, want 1 (no retry)", got)
			}
		})
	}
}

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
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"HTTP 200", `page title: "Making sure you're not a bot!"`, "bot-protection challenge"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
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

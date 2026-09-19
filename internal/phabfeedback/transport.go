package phabfeedback

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type transport interface {
	Request(method, target string, headers http.Header, data io.Reader) ([]byte, error)
}

type transportFunc func(method, target string, headers http.Header, data io.Reader) ([]byte, error)

func (f transportFunc) Request(method, target string, headers http.Header, data io.Reader) ([]byte, error) {
	return f(method, target, headers, data)
}

type httpTransport struct {
	client *http.Client
}

func (t httpTransport) Request(ctx context.Context, method, target string, headers http.Header, data io.Reader) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, data)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header = headers.Clone()
	response, err := t.client.Do(request)
	if err != nil {
		return nil, newNetworkError(0, "Request to %s failed: %v", safeLocation(target), err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, newNetworkError(response.StatusCode, "Could not read response from %s", safeLocation(target))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, newNetworkError(response.StatusCode, "HTTP %d from %s", response.StatusCode, safeLocation(target))
	}
	return body, nil
}

func safeLocation(target string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

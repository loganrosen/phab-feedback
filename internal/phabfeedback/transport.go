package phabfeedback

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type transport interface {
	Request(method, target string, headers http.Header, data io.Reader) ([]byte, error)
}

type httpTransport struct {
	client *http.Client
}

func (t httpTransport) Request(method, target string, headers http.Header, data io.Reader) ([]byte, error) {
	request, err := http.NewRequest(method, target, data)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header = headers.Clone()
	response, err := t.client.Do(request)
	if err != nil {
		return nil, newNetworkError(0, "Request to %s failed: %v", safeLocation(target), err)
	}
	defer response.Body.Close()
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

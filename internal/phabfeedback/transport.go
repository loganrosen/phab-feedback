package phabfeedback

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type transport interface {
	Request(method, target string, headers http.Header, data io.Reader) (transportResponse, error)
}

type transportFunc func(method, target string, headers http.Header, data io.Reader) (transportResponse, error)

func (f transportFunc) Request(
	method, target string,
	headers http.Header,
	data io.Reader,
) (transportResponse, error) {
	return f(method, target, headers, data)
}

type httpTransport struct {
	client *http.Client
}

type transportResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (t httpTransport) Request(
	ctx context.Context,
	method, target string,
	headers http.Header,
	data io.Reader,
) (transportResponse, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, data)
	if err != nil {
		return transportResponse{}, fmt.Errorf("create request: %w", err)
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "*/*")
	}
	response, err := t.client.Do(request)
	if err != nil {
		return transportResponse{}, newNetworkError(0, "Request to %s failed: %v", safeLocation(target), err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return transportResponse{}, newNetworkError(
			response.StatusCode,
			"Could not read response from %s",
			safeLocation(target),
		)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result := transportResponse{
			StatusCode: response.StatusCode,
			Header:     response.Header.Clone(),
			Body:       body,
		}
		if _, isHTML := htmlResponseDetails(result); isHTML {
			return transportResponse{}, htmlResponseError(target, result, "")
		}
		return transportResponse{}, newNetworkError(
			response.StatusCode,
			"HTTP %d from %s",
			response.StatusCode,
			safeLocation(target),
		)
	}
	return transportResponse{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       body,
	}, nil
}

func safeLocation(target string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

var titlePattern = regexp.MustCompile(`(?is)<title(?:\s[^>]*)?>(.*?)</title>`)

func htmlResponseDetails(response transportResponse) (string, bool) {
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	isHTML := contentType == "text/html" || contentType == "application/xhtml+xml"
	if !isHTML {
		isHTML = bytes.HasPrefix(bytes.TrimSpace(response.Body), []byte("<"))
	}
	if !isHTML {
		return "", false
	}
	sample := response.Body
	if len(sample) > 64*1024 {
		sample = sample[:64*1024]
	}
	title := ""
	if match := titlePattern.FindSubmatch(sample); len(match) == 2 {
		title = strings.Join(strings.Fields(html.UnescapeString(string(match[1]))), " ")
	}
	return title, true
}

func htmlResponseError(target string, response transportResponse, expected string) error {
	title, _ := htmlResponseDetails(response)
	message := fmt.Sprintf(
		"HTTP %d from %s returned HTML",
		response.StatusCode,
		safeLocation(target),
	)
	if expected != "" {
		message += " instead of " + expected
	}
	if title != "" {
		message += fmt.Sprintf(" (page title: %q)", title)
	}
	return newNetworkError(
		response.StatusCode,
		"%s; this may be a bot-protection challenge",
		message,
	)
}

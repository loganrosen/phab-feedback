package phabfeedback

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type conduitClient struct {
	host      string
	token     string
	transport transport
}

type conduitEnvelope struct {
	Result    json.RawMessage `json:"result"`
	ErrorCode *string         `json:"error_code"`
	ErrorInfo string          `json:"error_info"`
}

type searchPage struct {
	Data   []map[string]any `json:"data"`
	Cursor map[string]any   `json:"cursor"`
}

func (c *conduitClient) call(method string, params map[string]any, result any) error {
	conduitParams := cloneMap(params)
	conduitParams["__conduit__"] = map[string]any{"token": c.token}
	encodedParams, err := json.Marshal(conduitParams)
	if err != nil {
		return fmt.Errorf("encode Conduit request: %w", err)
	}
	form := url.Values{
		"params":      {string(encodedParams)},
		"output":      {"json"},
		"__conduit__": {"1"},
	}
	response, err := c.transport.Request(
		http.MethodPost,
		c.host+"/api/"+method,
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return err
	}
	if _, isHTML := htmlResponseDetails(response); isHTML {
		return htmlResponseError(c.host+"/api/"+method, response, "a Conduit response")
	}
	var envelope conduitEnvelope
	if err := decodeJSON(response.Body, "Conduit method "+method, &envelope); err != nil {
		return err
	}
	if envelope.ErrorCode != nil && *envelope.ErrorCode != "" {
		info := envelope.ErrorInfo
		if info == "" {
			info = "request rejected"
		}
		info = strings.ReplaceAll(info, c.token, "[redacted]")
		return fmt.Errorf("the Conduit method %s failed: %s: %s", method, *envelope.ErrorCode, info)
	}
	if len(envelope.Result) == 0 {
		return fmt.Errorf("the Conduit method %s returned no result", method)
	}
	if err := decodeJSON(envelope.Result, "Conduit "+method+" result", result); err != nil {
		return fmt.Errorf("the Conduit method %s returned invalid result data", method)
	}
	return nil
}

func (c *conduitClient) search(method string, constraints map[string]any, attachments map[string]any, order, after string, limit int) (searchPage, error) {
	params := map[string]any{"constraints": constraints}
	if len(attachments) > 0 {
		params["attachments"] = attachments
	}
	if order != "" {
		params["order"] = order
	}
	return c.paginate(method, params, after, limit)
}

func (c *conduitClient) paginate(method string, params map[string]any, after string, limit int) (searchPage, error) {
	var objects []map[string]any
	finalCursor := map[string]any{}
	nextAfter := after
	seen := map[string]bool{}
	if after != "" {
		seen[after] = true
	}
	for {
		pageParams := cloneMap(params)
		if nextAfter != "" {
			pageParams["after"] = nextAfter
		}
		if limit > 0 {
			remaining := limit - len(objects)
			if remaining <= 0 {
				break
			}
			pageParams["limit"] = min(remaining, 100)
		} else if _, ok := pageParams["limit"]; !ok {
			pageParams["limit"] = 100
		}
		var result searchPage
		if err := c.call(method, pageParams, &result); err != nil {
			return searchPage{}, err
		}
		page := result.Data
		if limit > 0 && len(objects)+len(page) > limit {
			page = page[:limit-len(objects)]
		}
		objects = append(objects, page...)
		cursor := result.Cursor
		if cursor == nil {
			cursor = map[string]any{}
		}
		finalCursor = cursor
		nextAfter = stringValue(cursor["after"])
		if nextAfter == "" || len(page) == 0 {
			break
		}
		if seen[nextAfter] {
			return searchPage{}, fmt.Errorf("%s returned a repeated cursor", method)
		}
		seen[nextAfter] = true
	}
	return searchPage{Data: objects, Cursor: finalCursor}, nil
}

type webClient struct {
	host               string
	cookie             string
	transport          transport
	csrfToken          string
	homepageLoaded     bool
	authenticated      bool
	reviewHelperLinked bool
}

var csrfPatterns = []*regexp.Regexp{
	regexp.MustCompile(`name="__csrf__"\s+value="(B@[A-Za-z0-9]+)"`),
	regexp.MustCompile(`"current":"(B@[A-Za-z0-9]+)"`),
	regexp.MustCompile(`"token":"(B@[A-Za-z0-9]+)"`),
}

func (w *webClient) csrf() (string, error) {
	if w.csrfToken != "" {
		return w.csrfToken, nil
	}
	response, err := w.transport.Request(http.MethodGet, w.host, http.Header{"Cookie": {w.cookie}}, nil)
	if err != nil {
		return "", err
	}
	body := response.Body
	w.homepageLoaded = true
	lowerBody := bytes.ToLower(body)
	w.authenticated = bytes.Contains(lowerBody, []byte("/logout/")) ||
		bytes.Contains(body, []byte(`"user":{"phid":"PHID-USER-`))
	w.reviewHelperLinked = bytes.Contains(lowerBody, []byte("reviewhelper"))
	decoded := html.UnescapeString(string(body))
	for _, pattern := range csrfPatterns {
		match := pattern.FindStringSubmatch(decoded)
		if len(match) == 2 {
			w.csrfToken = match[1]
			return w.csrfToken, nil
		}
	}
	message := fmt.Sprintf("could not extract a CSRF token from %s (HTTP %d", safeLocation(w.host), response.StatusCode)
	title, isHTML := htmlResponseDetails(response)
	if title != "" {
		message += fmt.Sprintf(", page title: %q", title)
	}
	message += ")"
	if isHTML {
		message += "; the server may have returned a bot-protection challenge"
	}
	return "", errors.New(message)
}

func (w *webClient) sessionAuthenticated() (bool, bool) {
	return w.authenticated, w.homepageLoaded
}

func (w *webClient) hasReviewHelper() (bool, bool) {
	return w.reviewHelperLinked, w.homepageLoaded
}

func (w *webClient) post(path string, values map[string]string) (map[string]any, error) {
	csrf, err := w.csrf()
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	for key, value := range values {
		form.Set(key, value)
	}
	response, err := w.transport.Request(
		http.MethodPost,
		w.host+path,
		http.Header{
			"Cookie":             {w.cookie},
			"X-Phabricator-Csrf": {csrf},
			"Content-Type":       {"application/x-www-form-urlencoded"},
		},
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, err
	}
	if _, isHTML := htmlResponseDetails(response); isHTML {
		return nil, htmlResponseError(w.host+path, response, "a JSON web response")
	}
	body := response.Body
	body = bytes.TrimPrefix(body, []byte("for (;;);"))
	payload, err := jsonObject(body, "Web endpoint "+path)
	if err != nil {
		return nil, err
	}
	if apiError := stringValue(payload["error"]); apiError != "" {
		return nil, fmt.Errorf("web endpoint %s failed: %s", path, apiError)
	}
	return payload, nil
}

func jsonObject(body []byte, operation string) (map[string]any, error) {
	var payload map[string]any
	if err := decodeJSON(body, operation, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, fmt.Errorf("%s returned an unexpected response", operation)
	}
	return payload, nil
}

func decodeJSON(body []byte, operation string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s returned an invalid JSON response", operation)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%s returned an invalid JSON response", operation)
	}
	return nil
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+1)
	maps.Copy(result, source)
	return result
}

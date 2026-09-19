package phabfeedback

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
)

type recordedRequest struct {
	method, target string
	headers        http.Header
	data           []byte
}

func (r recordedRequest) form(t *testing.T) url.Values {
	t.Helper()
	values, err := url.ParseQuery(string(r.data))
	if err != nil {
		t.Fatalf("parse form: %v", err)
	}
	return values
}

type fakeTransport struct {
	responses []any
	requests  []recordedRequest
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (f *fakeTransport) Request(method, target string, headers http.Header, data io.Reader) ([]byte, error) {
	var body []byte
	if data != nil {
		body, _ = io.ReadAll(data)
	}
	f.requests = append(f.requests, recordedRequest{method, target, headers.Clone(), body})
	if len(f.responses) == 0 {
		return nil, errors.New("unexpected request")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	if err, ok := response.(error); ok {
		return nil, err
	}
	responseBody, ok := response.([]byte)
	if !ok {
		return nil, fmt.Errorf("unexpected response type %T", response)
	}
	return responseBody, nil
}

func response(payload any) []byte {
	var body []byte
	if raw, ok := payload.([]byte); ok {
		body = raw
	} else {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			panic(fmt.Sprintf("marshal test response: %v", err))
		}
	}
	return body
}

func conduitResult(result any) []byte {
	return response(map[string]any{"result": result, "error_code": nil, "error_info": nil})
}

func transaction(transactionID int, kind string, comment int, fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	return map[string]any{
		"id": transactionID, "phid": "PHID-XACT-" + strconv.Itoa(transactionID), "type": kind, "fields": fields,
		"comments": []any{map[string]any{
			"id": comment, "phid": "PHID-CMT-" + strconv.Itoa(comment), "version": 1,
			"removed": false, "dateCreated": 100 + comment, "content": map[string]any{"raw": "comment " + strconv.Itoa(comment)},
		}},
	}
}

func revision(revisionID int) map[string]any {
	return map[string]any{
		"id": revisionID, "phid": "PHID-DREV-" + strconv.Itoa(revisionID),
		"fields": map[string]any{
			"title": "Revision " + strconv.Itoa(revisionID), "uri": "https://phab.example/D" + strconv.Itoa(revisionID),
			"authorPHID": "PHID-USER-author", "repositoryPHID": "PHID-REPO-main", "diffPHID": "PHID-DIFF-current",
			"status": map[string]any{"value": "needs-review", "name": "Needs Review"}, "isDraft": false,
			"dateCreated": 100, "dateModified": 200,
		},
		"attachments": map[string]any{"reviewers": map[string]any{"reviewers": []any{map[string]any{
			"reviewerPHID": "PHID-USER-reviewer", "actorPHID": "PHID-USER-reviewer", "status": "accepted", "isBlocking": true,
		}}}},
	}
}

func serviceWith(responses ...any) (*feedbackService, *fakeTransport) {
	transport := &fakeTransport{responses: responses}
	return &feedbackService{
		conduit: &conduitClient{host: "https://phab.example", token: "token", transport: transport},
		web:     &webClient{host: "https://phab.example", cookie: "phsid=cookie", transport: transport},
	}, transport
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

package phabfeedback

import "fmt"

type networkError struct {
	message string
	status  int
}

func (e *networkError) Error() string { return e.message }

func newNetworkError(status int, format string, args ...any) error {
	return &networkError{message: fmt.Sprintf(format, args...), status: status}
}

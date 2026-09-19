package phabfeedback

type mutationResultError struct {
	result any
	err    error
}

func (e *mutationResultError) Error() string {
	return e.err.Error()
}

func (e *mutationResultError) Unwrap() error {
	return e.err
}

func (e *mutationResultError) commandResult() any {
	return e.result
}

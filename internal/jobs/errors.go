package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
)

// Error is a job failure with a stable Problem code. Return one (wrapped or
// not) from Run to fail the job with that code.
type Error struct {
	Status int    // HTTP-style status for the Problem
	Code   string // e.g. "insufficient_storage"
	Title  string
	Detail string
	Err    error // optional cause
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Err }

// Fail returns an *Error.
func Fail(status int, code, title, detail string) *Error {
	return &Error{Status: status, Code: code, Title: title, Detail: detail}
}

// problem turns a Run error into Problem JSON: *Error keeps its code; known
// errors map to theirs; anything else is internal_error with its text.
func problem(err error) json.RawMessage {
	var je *Error
	p := map[string]any{"type": "about:blank"}
	switch {
	case errors.As(err, &je):
		p["status"], p["code"], p["title"] = je.Status, je.Code, je.Title
		if je.Detail != "" {
			p["detail"] = je.Detail
		}
	case errors.Is(err, cas.ErrInsufficientStorage):
		p["status"], p["code"], p["title"] = http.StatusInsufficientStorage, "insufficient_storage", "Not enough storage"
		p["detail"] = err.Error()
	default:
		p["status"], p["code"], p["title"] = http.StatusInternalServerError, "internal_error", "Job failed"
		p["detail"] = err.Error()
	}
	if p["title"] == "" {
		p["title"] = "Job failed"
	}
	b, _ := json.Marshal(p)
	return b
}

// interrupted is the error of a job the server stopped under.
var interrupted = problem(Fail(http.StatusServiceUnavailable, "interrupted", "Interrupted",
	"The server stopped while this job was running. Start it again."))

// isCancel reports a context cancellation (as opposed to a real failure).
func isCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

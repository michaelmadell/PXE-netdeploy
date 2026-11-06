package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// problem is an RFC 9457 error body. It mirrors the Problem schema; it is
// hand-written here so error paths work even before an operation is generated.
type problem struct {
	Type   string         `json:"type"`
	Title  string         `json:"title"`
	Status int            `json:"status"`
	Detail string         `json:"detail,omitempty"`
	Code   string         `json:"code"`
	Errors []problemField `json:"errors,omitempty"`
}

type problemField struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func writeProblem(w http.ResponseWriter, status int, code, title, detail string) {
	writeProblemFields(w, status, code, title, detail, nil)
}

func writeProblemFields(w http.ResponseWriter, status int, code, title, detail string, fields []fieldError) {
	p := problem{Type: "about:blank", Title: title, Status: status, Detail: detail, Code: code}
	for _, f := range fields {
		p.Errors = append(p.Errors, problemField{Field: f.field, Message: f.message})
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// RequestError handles requests the generated code could not decode or validate.
func RequestError(w http.ResponseWriter, _ *http.Request, err error) {
	writeProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error())
}

// ResponseError handles errors returned by operation implementations.
// Details are logged, not sent: internal errors can contain paths or secrets.
func ResponseError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("operation failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal error", "")
}

// NotFound answers unknown /api paths with a JSON problem.
func NotFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, http.StatusNotFound, "not_found", "Not found", "No API operation at "+r.URL.Path)
}

// Recover converts panics (including calls to not-yet-implemented
// operations, see Server) into a 500 problem and logs the stack.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal error",
					"The operation failed or is not implemented yet.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush keeps server-sent events working through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Logging writes one structured line per request.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

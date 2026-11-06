package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
)

// GetSettings implements GET /settings.
func (s *Server) GetSettings(_ context.Context, _ api.GetSettingsRequestObject) (api.GetSettingsResponseObject, error) {
	out, tag, err := s.settings.Get()
	if err != nil {
		return nil, err
	}
	return api.GetSettings200JSONResponse{Body: out, Headers: api.GetSettings200ResponseHeaders{ETag: tag}}, nil
}

// ReplaceSettings implements PUT /settings.
func (s *Server) ReplaceSettings(ctx context.Context, req api.ReplaceSettingsRequestObject) (api.ReplaceSettingsResponseObject, error) {
	if req.Body == nil {
		return api.ReplaceSettings400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "A JSON body is required.", nil))}, nil
	}
	out, tag, err := s.settings.Replace(ctx, *req.Body, req.Params.IfMatch)

	var verr *settings.ValidationError
	var berr *netsvc.BindError
	switch {
	case err == nil:
		s.events.Publish(events.Event{Type: events.SettingsUpdated, Data: map[string]string{"etag": tag}})
		s.events.Publish(events.Event{Type: events.HealthUpdated, Data: s.health(ctx)})
		return api.ReplaceSettings200JSONResponse{Body: out, Headers: api.ReplaceSettings200ResponseHeaders{ETag: tag}}, nil
	case errors.Is(err, settings.ErrPreconditionFailed):
		return api.ReplaceSettings412ApplicationProblemPlusJSONResponse(newProblem(http.StatusPreconditionFailed,
			"etag_mismatch", "Precondition failed", "Settings changed since they were read; GET them again and reapply your change.", nil)), nil
	case errors.As(err, &verr):
		fields := make([]fieldError, len(verr.Fields))
		for i, f := range verr.Fields {
			fields[i] = fieldError{f.Field, f.Message}
		}
		return api.ReplaceSettings422ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnprocessableEntity,
			"invalid_settings", "Settings can't be applied", "", fields)), nil
	case errors.As(err, &berr):
		fields := make([]fieldError, len(berr.Failures))
		for i, f := range berr.Failures {
			fields[i] = fieldError{f.Field, fmt.Sprintf("%s %s/%d on %s: %s", f.Service, f.Protocol, f.Port, f.Interface, f.Message)}
		}
		slog.Warn("settings not applied: bind failed", "err", err)
		return api.ReplaceSettings422ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnprocessableEntity,
			"bind_failed", "Settings can't be applied", "A listener could not be started; the running configuration is unchanged.", fields)), nil
	}
	return nil, err
}

type fieldError struct{ field, message string }

// newProblem builds an api.Problem with optional field errors.
func newProblem(status int, code, title, detail string, fields []fieldError) api.Problem {
	p := api.Problem{Type: "about:blank", Title: title, Status: status, Code: code}
	if detail != "" {
		p.Detail = &detail
	}
	if len(fields) > 0 {
		errs := make([]struct {
			Field   *string `json:"field,omitempty"`
			Message *string `json:"message,omitempty"`
		}, len(fields))
		for i, f := range fields {
			errs[i].Field, errs[i].Message = ptr(f.field), ptr(f.message)
		}
		p.Errors = &errs
	}
	return p
}

func ptr[T any](v T) *T { return &v }

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// apiBase is the spec's server URL; requests below it are validated.
const apiBase = "/api/v1"

// RequestValidator checks every /api/v1 request against the spec (ranges,
// enums, patterns, required fields; the generated code only checks types)
// and answers violations with a 400 problem listing each field. Read-only
// properties in request bodies are ignored, as the spec promises. Requests
// for unknown routes pass through to the router's own 404.
func RequestValidator(spec *openapi3.T) (func(http.Handler) http.Handler, error) {
	// Match paths without the server prefix: the spec's server URL is
	// relative, and the router doesn't need to know where it is mounted.
	doc := *spec
	doc.Servers = nil
	router, err := legacy.NewRouter(&doc)
	if err != nil {
		return nil, fmt.Errorf("request validator: %w", err)
	}
	opts := &openapi3filter.Options{
		MultiError:                 true,
		ExcludeReadOnlyValidations: true,
		// Authentication is enforced separately (not implemented yet).
		AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, ok := strings.CutPrefix(r.URL.Path, apiBase)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			rr := r.Clone(r.Context())
			rr.URL.Path, rr.URL.RawPath = path, ""
			route, params, err := router.FindRoute(rr)
			if err != nil {
				next.ServeHTTP(w, r) // unknown path or method: let the router answer
				return
			}
			in := &openapi3filter.RequestValidationInput{Request: rr, PathParams: params, Route: route, Options: opts}
			if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil {
				writeValidationProblem(w, err)
				return
			}
			// ValidateRequest consumed and restored rr's body; hand it on.
			r.Body = rr.Body
			next.ServeHTTP(w, r)
		})
	}, nil
}

// Make sure the legacy router keeps satisfying the interface we rely on.
var _ routers.Router = (*legacy.Router)(nil)

func writeValidationProblem(w http.ResponseWriter, err error) {
	var fields []fieldError
	collectFieldErrors(err, "", &fields)
	if len(fields) == 0 {
		fields = append(fields, fieldError{"", err.Error()})
	}
	writeProblemFields(w, http.StatusBadRequest, "invalid_request", "Invalid request",
		"The request doesn't match the API schema.", fields)
}

// collectFieldErrors flattens kin-openapi's nested errors into field paths
// like "interfaces[0].dhcp.mode" (body) or "header If-Match" (parameter).
// It walks the concrete types: errors.As would look through a RequestError
// to the MultiError inside it and lose the parameter name.
func collectFieldErrors(err error, prefix string, out *[]fieldError) {
	switch e := err.(type) {
	case openapi3.MultiError:
		for _, x := range e {
			collectFieldErrors(x, prefix, out)
		}
	case *openapi3filter.RequestError:
		p := prefix
		if e.Parameter != nil {
			p = e.Parameter.In + " " + e.Parameter.Name
		}
		before := len(*out)
		if e.Err != nil {
			collectFieldErrors(e.Err, p, out)
		}
		if len(*out) == before {
			msg := e.Reason
			if msg == "" && e.Err != nil {
				msg = e.Err.Error()
			}
			*out = append(*out, fieldError{p, msg})
		}
	case *openapi3.SchemaError:
		field := strings.TrimSpace(prefix + " " + jsonPath(e.JSONPointer()))
		msg := e.Reason
		if msg == "" {
			msg = "invalid value"
		}
		*out = append(*out, fieldError{field, msg})
	case *openapi3filter.ParseError:
		*out = append(*out, fieldError{prefix, e.Error()})
	default:
		if prefix != "" {
			*out = append(*out, fieldError{prefix, err.Error()})
		}
	}
}

// jsonPath renders ["interfaces","0","dhcp"] as "interfaces[0].dhcp".
func jsonPath(ptr []string) string {
	var b strings.Builder
	for _, p := range ptr {
		if p != "" && strings.Trim(p, "0123456789") == "" {
			b.WriteString("[" + p + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(p)
	}
	return b.String()
}

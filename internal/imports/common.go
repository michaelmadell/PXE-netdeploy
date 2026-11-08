package imports

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
)

type params struct {
	UploadID string `json:"uploadId"`
}

// failure releases the upload a failed import used (ADR 0017: kept for a
// retry until its expiry) and returns the job error, its detail saying
// until when.
func failure(ctx context.Context, up *uploads.Manager, jobID, uploadID string, cause error) *jobs.Error {
	e := jobs.AsError(cause)
	if u, err := up.Release(ctx, uploadID, jobID, false); err == nil {
		switch {
		case u.Imported:
			e.Detail = strings.TrimSpace(e.Detail + " The upload is kept until it is deleted.")
		case u.ExpiresAt != nil:
			e.Detail = strings.TrimSpace(fmt.Sprintf("%s The upload is kept until %s for a retry: import it again before then.",
				e.Detail, u.ExpiresAt.UTC().Format(time.RFC3339)))
		}
	}
	return e
}

// abandoned is the error recorded for an import that never finished its
// own cleanup (jobs.Kind.Abandon).
func abandoned(reason string) *jobs.Error {
	e := jobs.Fail(http.StatusServiceUnavailable, reason, "Import "+reason, "")
	switch reason {
	case "interrupted":
		e.Detail = "The server stopped during the import."
	case "cancelled":
		e.Status, e.Detail = http.StatusConflict, "The import was cancelled before it started."
	case "failed":
		e.Code, e.Status, e.Detail = "internal_error", http.StatusInternalServerError, "The import failed unexpectedly."
	}
	return e
}

// cancelledError is recorded when the admin cancels a running import.
func cancelledError() error {
	return jobs.Fail(http.StatusConflict, "cancelled", "Import cancelled", "The import was cancelled.")
}

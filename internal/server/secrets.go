package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
)

// MaxSecretFileBytes caps file-based secrets (ODJ blobs, Autopilot JSON,
// provisioning packages), which are held in the database.
const MaxSecretFileBytes = 16 << 20

// StateKeyExport records the last `netdeployd key export` (fingerprint, time).
const StateKeyExport = "secrets.keyExport"

// KeyExport is the StateKeyExport value.
type KeyExport struct {
	Fingerprint string `json:"fingerprint"`
	At          string `json:"at"`
}

var productKey = regexp.MustCompile(`^[0-9A-Z]{5}(-[0-9A-Z]{5}){4}$`)

func secretProblem(status int, code, detail string) api.Problem {
	title := map[int]string{400: "Invalid request", 404: "Not found", 409: "Conflict", 412: "Precondition failed"}[status]
	return newProblem(status, code, title, detail, nil)
}

// secretValue turns a SecretValue into the bytes to seal and the hint, per
// kind. Upload-based values consume (delete) their upload.
func (s *Server) secretValue(ctx context.Context, kind api.SecretKind, credential *struct {
	Password string `json:"password"`
	Username string `json:"username"`
}, uploadID, value *string) ([]byte, *string, *api.Problem) {
	bad := func(detail string) ([]byte, *string, *api.Problem) {
		p := secretProblem(400, "invalid_request", detail)
		return nil, nil, &p
	}
	n := 0
	for _, set := range []bool{credential != nil, uploadID != nil, value != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return bad("Give exactly one of value, credential or uploadId.")
	}
	switch kind {
	case api.SecretKindPassword, api.SecretKindProductKey:
		if value == nil || *value == "" {
			return bad(string(kind) + " secrets take a non-empty value.")
		}
		v := *value
		if kind == api.SecretKindProductKey {
			v = strings.ToUpper(strings.TrimSpace(v))
			if !productKey.MatchString(v) {
				return bad("A product key is five groups of five letters or digits: XXXXX-XXXXX-XXXXX-XXXXX-XXXXX.")
			}
		}
		return []byte(v), nil, nil
	case api.SecretKindDomainCredential, api.SecretKindShareCredential:
		if credential == nil || credential.Username == "" || credential.Password == "" {
			return bad(string(kind) + " secrets take a credential with a username and password.")
		}
		u := credential.Username
		return secrets.Credential{Username: u, Password: credential.Password}.Encode(), &u, nil
	case api.SecretKindOdjBlob, api.SecretKindAutopilotJson, api.SecretKindProvisioningPackage:
		if uploadID == nil {
			return bad(string(kind) + " secrets take an uploadId.")
		}
		if s.uploads == nil {
			return bad("Uploads are not available.")
		}
		b, name, err := s.uploads.Consume(ctx, *uploadID, MaxSecretFileBytes)
		switch {
		case errors.Is(err, store.ErrNotFound):
			p := secretProblem(400, "invalid_request", "No upload "+*uploadID+".")
			return nil, nil, &p
		case errors.Is(err, store.ErrUploadIncomplete):
			return bad("The upload hasn't received all its bytes yet.")
		case errors.Is(err, store.ErrUploadInUse):
			return bad("Another import is using this upload.")
		case errors.Is(err, uploads.ErrTooLarge):
			return bad("Secret files are at most 16 MiB.")
		case err != nil:
			p := newProblem(http.StatusInternalServerError, "internal_error", "Internal error", err.Error(), nil)
			return nil, nil, &p
		}
		if kind == api.SecretKindAutopilotJson && !json.Valid(b) {
			return bad("The Autopilot file isn't valid JSON.")
		}
		var hint *string
		if name != "" {
			hint = &name
		}
		return b, hint, nil
	}
	return bad("Unknown kind " + string(kind) + ".")
}

func secretToAPI(x store.Secret) api.Secret {
	tag := versionETag(x.Version)
	out := api.Secret{Id: &x.ID, Etag: &tag, CreatedAt: &x.CreatedAt, UpdatedAt: &x.UpdatedAt, Name: x.Name,
		Kind: api.SecretKind(x.Kind), Hint: x.Hint, ValueUpdatedAt: x.ValueUpdatedAt}
	if x.Description != "" {
		out.Description = &x.Description
	}
	return out
}

func validName(n string) bool { return n != "" && len(n) <= 200 }

// CreateSecret implements POST /secrets.
func (s *Server) CreateSecret(ctx context.Context, req api.CreateSecretRequestObject) (api.CreateSecretResponseObject, error) {
	b := req.Body
	if b == nil || !validName(b.Name) {
		return api.CreateSecret400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("name must be 1 to 200 characters.")}, nil
	}
	plain, hint, p := s.secretValue(ctx, b.Kind, b.Credential, b.UploadId, b.Value)
	if p != nil {
		return api.CreateSecret400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
	}
	desc := ""
	if b.Description != nil {
		desc = *b.Description
	}
	x, err := s.vault.Create(ctx, b.Name, desc, string(b.Kind), plain, hint)
	if err != nil {
		return nil, err
	}
	return api.CreateSecret201JSONResponse(secretToAPI(x)), nil
}

// ListSecrets implements GET /secrets.
func (s *Server) ListSecrets(ctx context.Context, req api.ListSecretsRequestObject) (api.ListSecretsResponseObject, error) {
	limit, after, kind := 0, "", ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		after = *req.Params.Cursor
	}
	if req.Params.Kind != nil {
		kind = string(*req.Params.Kind)
	}
	list, next, err := s.store.ListSecrets(ctx, kind, limit, after)
	if err != nil {
		return nil, err
	}
	out := api.ListSecrets200JSONResponse{Items: make([]api.Secret, len(list))}
	for i, x := range list {
		out.Items[i] = secretToAPI(x)
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetSecret implements GET /secrets/{secretId}.
func (s *Server) GetSecret(ctx context.Context, req api.GetSecretRequestObject) (api.GetSecretResponseObject, error) {
	x, err := s.store.Secret(ctx, req.SecretId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetSecret404ApplicationProblemPlusJSONResponse(secretProblem(404, "not_found", "No secret "+req.SecretId+".")), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetSecret200JSONResponse(secretToAPI(x)), nil
}

// UpdateSecretMetadata implements PATCH /secrets/{secretId}.
func (s *Server) UpdateSecretMetadata(ctx context.Context, req api.UpdateSecretMetadataRequestObject) (api.UpdateSecretMetadataResponseObject, error) {
	b := req.Body
	if b == nil || (b.Name != nil && !validName(*b.Name)) {
		return api.UpdateSecretMetadata400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("name must be 1 to 200 characters.")}, nil
	}
	v, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.UpdateSecretMetadata412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	x, err := s.store.UpdateSecretMetadata(ctx, req.SecretId, b.Name, b.Description, v)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.UpdateSecretMetadata404ApplicationProblemPlusJSONResponse(secretProblem(404, "not_found", "No secret "+req.SecretId+".")), nil
	case errors.Is(err, store.ErrVersionMismatch):
		return api.UpdateSecretMetadata412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	case err != nil:
		return nil, err
	}
	return api.UpdateSecretMetadata200JSONResponse(secretToAPI(x)), nil
}

// ReplaceSecretValue implements PUT /secrets/{secretId}/value.
func (s *Server) ReplaceSecretValue(ctx context.Context, req api.ReplaceSecretValueRequestObject) (api.ReplaceSecretValueResponseObject, error) {
	cur, err := s.store.Secret(ctx, req.SecretId)
	if errors.Is(err, store.ErrNotFound) {
		return api.ReplaceSecretValue404ApplicationProblemPlusJSONResponse(secretProblem(404, "not_found", "No secret "+req.SecretId+".")), nil
	}
	if err != nil {
		return nil, err
	}
	v, ok := parseETag(req.Params.IfMatch)
	if !ok || v != cur.Version {
		// Checked before an upload is consumed.
		return api.ReplaceSecretValue412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	if req.Body == nil {
		return api.ReplaceSecretValue400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	plain, hint, p := s.secretValue(ctx, api.SecretKind(cur.Kind), req.Body.Credential, req.Body.UploadId, req.Body.Value)
	if p != nil {
		return api.ReplaceSecretValue400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(*p)}, nil
	}
	x, err := s.vault.Replace(ctx, req.SecretId, plain, hint, v)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.ReplaceSecretValue404ApplicationProblemPlusJSONResponse(secretProblem(404, "not_found", "No secret "+req.SecretId+".")), nil
	case errors.Is(err, store.ErrVersionMismatch):
		return api.ReplaceSecretValue412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	case err != nil:
		return nil, err
	}
	return api.ReplaceSecretValue200JSONResponse(secretToAPI(x)), nil
}

// DeleteSecret implements DELETE /secrets/{secretId}.
func (s *Server) DeleteSecret(ctx context.Context, req api.DeleteSecretRequestObject) (api.DeleteSecretResponseObject, error) {
	err := s.store.DeleteSecret(ctx, req.SecretId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteSecret404ApplicationProblemPlusJSONResponse(secretProblem(404, "not_found", "No secret "+req.SecretId+".")), nil
	case errors.Is(err, store.ErrSecretInUse):
		return api.DeleteSecret409ApplicationProblemPlusJSONResponse(secretProblem(409, "secret_in_use",
			"The secret is in use: "+strings.TrimPrefix(err.Error(), store.ErrSecretInUse.Error()+": ")+".")), nil
	case err != nil:
		return nil, err
	}
	return api.DeleteSecret204Response{}, nil
}

// secretsWarnings: secrets_key_not_exported while secrets exist and the
// current key has never been exported.
func (s *Server) secretsWarnings(ctx context.Context) []warning {
	if s.store == nil || s.vault == nil {
		return nil
	}
	n, err := s.store.CountSecrets(ctx)
	if err != nil || n == 0 {
		return nil
	}
	r, err := s.vault.Keys().Ring(false)
	if err != nil {
		return []warning{{"secrets_key_unreadable", "The secrets key can't be read: " + err.Error()}}
	}
	var ex KeyExport
	if ok, _ := s.store.State(ctx, StateKeyExport, &ex); ok && ex.Fingerprint == r.Fingerprint() {
		return nil
	}
	return []warning{{"secrets_key_not_exported", "The secrets key has never been exported. Run `netdeployd key export` and keep the file safe: without it, secrets can't be restored on other hardware."}}
}

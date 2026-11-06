package server

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// ListClients implements GET /clients.
func (s *Server) ListClients(ctx context.Context, req api.ListClientsRequestObject) (api.ListClientsResponseObject, error) {
	p := req.Params
	f := store.ClientFilter{}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	if p.Cursor != nil {
		f.After = *p.Cursor
	}
	if p.Mac != nil {
		f.MAC = *p.Mac
	}
	if p.ProfileId != nil {
		f.ProfileID = *p.ProfileId
	}
	if p.Source != nil {
		f.Source = string(*p.Source)
	}
	cs, next, err := s.store.ListClients(ctx, f)
	if err != nil {
		if errors.Is(err, store.ErrInvalidMAC) {
			return api.ListClients400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
				newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "mac is not a MAC address.", nil))}, nil
		}
		return nil, err
	}
	out := api.ListClients200JSONResponse{Items: make([]api.Client, len(cs))}
	for i, c := range cs {
		out.Items[i] = clientToAPI(c)
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// CreateClient implements POST /clients.
func (s *Server) CreateClient(ctx context.Context, req api.CreateClientRequestObject) (api.CreateClientResponseObject, error) {
	if req.Body == nil {
		return api.CreateClient400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "A JSON body is required.", nil))}, nil
	}
	in, fields := clientFromAPI(*req.Body)
	if len(fields) > 0 {
		return api.CreateClient422ApplicationProblemPlusJSONResponse(unprocessable(fields)), nil
	}
	c, err := s.store.CreateClient(ctx, in)
	if p, ok := clientConflict(err); ok {
		return api.CreateClient409ApplicationProblemPlusJSONResponse(p), nil
	}
	if err != nil {
		return nil, err
	}
	s.clientEvent(c.ID, c.MAC, "created")
	return api.CreateClient201JSONResponse(clientToAPI(c)), nil
}

// GetClient implements GET /clients/{clientId}.
func (s *Server) GetClient(ctx context.Context, req api.GetClientRequestObject) (api.GetClientResponseObject, error) {
	c, err := s.store.Client(ctx, req.ClientId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetClient404ApplicationProblemPlusJSONResponse(clientNotFound(req.ClientId)), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetClient200JSONResponse(clientToAPI(c)), nil
}

// ReplaceClient implements PUT /clients/{clientId}.
func (s *Server) ReplaceClient(ctx context.Context, req api.ReplaceClientRequestObject) (api.ReplaceClientResponseObject, error) {
	if req.Body == nil {
		return api.ReplaceClient400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "A JSON body is required.", nil))}, nil
	}
	version, ok := parseETag(req.Params.IfMatch)
	if !ok {
		return api.ReplaceClient412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	in, fields := clientFromAPI(*req.Body)
	if len(fields) > 0 {
		return api.ReplaceClient422ApplicationProblemPlusJSONResponse(unprocessable(fields)), nil
	}
	c, err := s.store.ReplaceClient(ctx, req.ClientId, in, version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.ReplaceClient404ApplicationProblemPlusJSONResponse(clientNotFound(req.ClientId)), nil
	case errors.Is(err, store.ErrVersionMismatch):
		return api.ReplaceClient412ApplicationProblemPlusJSONResponse(etagMismatch()), nil
	}
	if p, ok := clientConflict(err); ok {
		return api.ReplaceClient409ApplicationProblemPlusJSONResponse(p), nil
	}
	if err != nil {
		return nil, err
	}
	s.clientEvent(c.ID, c.MAC, "updated")
	return api.ReplaceClient200JSONResponse(clientToAPI(c)), nil
}

// DeleteClient implements DELETE /clients/{clientId}.
func (s *Server) DeleteClient(ctx context.Context, req api.DeleteClientRequestObject) (api.DeleteClientResponseObject, error) {
	mac := ""
	if c, err := s.store.Client(ctx, req.ClientId); err == nil {
		mac = c.MAC
	}
	err := s.store.DeleteClient(ctx, req.ClientId)
	switch {
	case err == nil:
		s.clientEvent(req.ClientId, mac, "deleted")
		return api.DeleteClient204Response{}, nil
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteClient404ApplicationProblemPlusJSONResponse(clientNotFound(req.ClientId)), nil
	case errors.Is(err, store.ErrDeploymentActive):
		return api.DeleteClient409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "deployment_active",
			"Deployment active", "The client has a pending or running deployment; cancel it first.", nil)), nil
	}
	return nil, err
}

// clientFromAPI converts and checks what the schema can't: reservedIp is an
// IPv4 address, and fields naming things that don't exist yet are null.
func clientFromAPI(in api.ClientInput) (store.ClientInput, []fieldError) {
	var fields []fieldError
	out := store.ClientInput{
		MAC: in.Mac, Serial: in.Serial, Hostname: in.Hostname, ProfileID: in.ProfileId, ReservedIP: in.ReservedIp,
	}
	if _, err := store.NormalizeMAC(in.Mac); err != nil {
		fields = append(fields, fieldError{"mac", "not a 48-bit MAC address"})
	}
	if in.SmbiosUuid != nil {
		u := in.SmbiosUuid.String()
		out.SMBIOSUUID = &u
	}
	if in.Variables != nil {
		out.Variables = *in.Variables
	}
	if in.ReservedIp != nil {
		a, err := netip.ParseAddr(*in.ReservedIp)
		if err != nil || !a.Is4() {
			fields = append(fields, fieldError{"reservedIp", "must be an IPv4 address"})
		} else {
			canon := a.String()
			out.ReservedIP = &canon
		}
	}
	if in.ProfileId != nil {
		fields = append(fields, fieldError{"profileId", "must be null until profiles exist (Phase 4)"})
	}
	if in.SecretRefs != nil && in.SecretRefs.OdjBlob != nil {
		fields = append(fields, fieldError{"secretRefs.odjBlob", "must be null until secrets exist (Phase 5)"})
	}
	return out, fields
}

func clientToAPI(c store.Client) api.Client {
	etag := strconv.Quote(strconv.FormatInt(c.Version, 10))
	source := api.ClientSource(c.Source)
	out := api.Client{
		Id: &c.ID, Etag: &etag, CreatedAt: &c.CreatedAt, UpdatedAt: &c.UpdatedAt,
		Mac: c.MAC, Hostname: c.Hostname, Serial: c.Serial, ProfileId: c.ProfileID, ReservedIp: c.ReservedIP,
		Source: &source, LastSeenAt: c.LastSeenAt, LastIp: c.LastIP,
	}
	vars := api.Variables(c.Variables)
	if vars == nil {
		vars = api.Variables{}
	}
	out.Variables = &vars
	if c.SMBIOSUUID != nil {
		var u openapi_types.UUID
		if err := u.UnmarshalText([]byte(*c.SMBIOSUUID)); err == nil {
			out.SmbiosUuid = &u
		}
	}
	if c.LastArch != nil {
		a := api.ClientArch(*c.LastArch)
		out.LastArch = &a
	}
	return out
}

// parseETag reads a single strong ETag `"N"` from If-Match.
func parseETag(h string) (int64, bool) {
	h = strings.TrimSpace(h)
	unq, err := strconv.Unquote(h)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(unq, 10, 64)
	return v, err == nil
}

func clientConflict(err error) (api.Problem, bool) {
	switch {
	case errors.Is(err, store.ErrMACTaken):
		return newProblem(http.StatusConflict, "mac_taken", "MAC already registered", "Another client has this MAC.", nil), true
	case errors.Is(err, store.ErrReservedIPTaken):
		return newProblem(http.StatusConflict, "reserved_ip_taken", "Address already reserved", "Another client has this reservedIp.", nil), true
	}
	return api.Problem{}, false
}

func clientNotFound(id string) api.Problem {
	return newProblem(http.StatusNotFound, "not_found", "Not found", "No client "+id+".", nil)
}

func etagMismatch() api.Problem {
	return newProblem(http.StatusPreconditionFailed, "etag_mismatch", "Precondition failed",
		"The client changed since it was read; GET it again and reapply your change.", nil)
}

func unprocessable(fields []fieldError) api.Problem {
	return newProblem(http.StatusUnprocessableEntity, "invalid_client", "Client can't be saved", "", fields)
}

func (s *Server) clientEvent(id, mac, change string) {
	s.events.Publish(events.Event{Type: events.ClientUpdated, ResourceID: id,
		Data: map[string]string{"id": id, "mac": mac, "change": change}})
}

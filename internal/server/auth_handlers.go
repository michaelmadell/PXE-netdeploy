package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// ---------------------------------------------------------------- session

// GetSession implements GET /auth/session.
func (s *Server) GetSession(ctx context.Context, _ api.GetSessionRequestObject) (api.GetSessionResponseObject, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := api.Session{SetupRequired: n == 0}
	if p, ok := principalFrom(ctx); ok {
		u, err := s.store.UserByID(ctx, p.UserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			user := userToAPI(u)
			via := api.SessionVia(p.Via)
			out.SignedIn, out.User, out.Via = true, &user, &via
			if p.Via == "cookie" {
				exp := p.ExpiresAt
				out.ExpiresAt = &exp
			}
		}
	}
	return api.GetSession200JSONResponse(out), nil
}

// SignIn implements POST /auth/sign-in.
func (s *Server) SignIn(ctx context.Context, req api.SignInRequestObject) (api.SignInResponseObject, error) {
	if req.Body == nil {
		return api.SignIn400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	if n, err := s.store.CountUsers(ctx); err != nil {
		return nil, err
	} else if n == 0 {
		return api.SignIn409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "setup_required", "No account yet",
			"Create the first account with the setup code.", nil)), nil
	}
	name := strings.TrimSpace(req.Body.Username)
	keys := []string{"user:" + strings.ToLower(name), "ip:" + remoteIP(ctx).String()}
	now := s.auth.clock()
	if wait := s.auth.lim.blocked(now, keys...); wait > 0 {
		return api.SignIn429ApplicationProblemPlusJSONResponse{TooManyAttemptsApplicationProblemPlusJSONResponse: tooMany(wait)}, nil
	}
	u, err := s.store.UserByName(ctx, name)
	hash := u.PasswordHash
	if errors.Is(err, store.ErrNotFound) {
		hash = dummyHash()
	} else if err != nil {
		return nil, err
	}
	ok, verr := settings.VerifyPassword(hash, req.Body.Password)
	if verr != nil || !ok || u.ID == "" {
		s.auth.lim.fail(now, keys...)
		slog.Warn("admin sign-in failed", "username", name, "ip", remoteIP(ctx).String())
		return api.SignIn401ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnauthorized, "bad_credentials", "Wrong username or password", "", nil)), nil
	}
	s.auth.lim.succeed(keys...)
	cookie, sess, err := s.startSession(ctx, u)
	if err != nil {
		return nil, err
	}
	slog.Info("admin signed in", "username", u.Username, "ip", remoteIP(ctx).String())
	return sessionResponse{status: http.StatusOK, session: &sess, cookie: cookie}, nil
}

// SignOut implements POST /auth/sign-out.
func (s *Server) SignOut(ctx context.Context, _ api.SignOutRequestObject) (api.SignOutResponseObject, error) {
	p, ok := principalFrom(ctx)
	if !ok || p.Via != "cookie" {
		return api.SignOut401ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
			newProblem(http.StatusUnauthorized, "unauthenticated", "Not signed in", "", nil))}, nil
	}
	if err := s.store.DeleteAdminSession(ctx, p.SessionHash); err != nil {
		return nil, err
	}
	s.endSessions(sessionEnd{Reason: endSignedOut, Session: p.SessionHash}) // other tabs
	slog.Info("admin signed out", "username", p.Username)
	return sessionResponse{status: http.StatusNoContent, cookie: s.sessionCookie("", s.auth.clock())}, nil
}

// SetUp implements POST /auth/setup.
func (s *Server) SetUp(ctx context.Context, req api.SetUpRequestObject) (api.SetUpResponseObject, error) {
	if req.Body == nil {
		return api.SetUp400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	alreadySetUp := api.SetUp409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "already_set_up", "Already set up",
		"An account exists: sign in instead.", nil))
	if n, err := s.store.CountUsers(ctx); err != nil {
		return nil, err
	} else if n > 0 {
		return alreadySetUp, nil
	}
	keys := []string{"setup:" + remoteIP(ctx).String()}
	now := s.auth.clock()
	if wait := s.auth.lim.blocked(now, keys...); wait > 0 {
		return api.SetUp429ApplicationProblemPlusJSONResponse{TooManyAttemptsApplicationProblemPlusJSONResponse: tooMany(wait)}, nil
	}
	if !s.auth.setup.check(req.Body.SetupCode) {
		s.auth.lim.fail(now, keys...)
		slog.Warn("wrong setup code", "ip", remoteIP(ctx).String())
		return api.SetUp403ApplicationProblemPlusJSONResponse(newProblem(http.StatusForbidden, "bad_setup_code", "Wrong setup code",
			"The setup code is in the server's log and in setup-code.txt in its data directory.", nil)), nil
	}
	s.auth.lim.succeed(keys...)
	hash, err := settings.HashPassword(req.Body.Password)
	if err != nil {
		return nil, err
	}
	u, err := s.store.CreateFirstUser(ctx, req.Body.Username, hash)
	if errors.Is(err, store.ErrAlreadySetUp) {
		return alreadySetUp, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.auth.setup.done(); err != nil {
		slog.Warn("setup code file not deleted", "err", err)
	}
	cookie, sess, err := s.startSession(ctx, u)
	if err != nil {
		return nil, err
	}
	slog.Info("first admin account created", "username", u.Username, "ip", remoteIP(ctx).String())
	return sessionResponse{status: http.StatusCreated, session: &sess, cookie: cookie}, nil
}

// ChangeOwnPassword implements PUT /auth/session/password.
func (s *Server) ChangeOwnPassword(ctx context.Context, req api.ChangeOwnPasswordRequestObject) (api.ChangeOwnPasswordResponseObject, error) {
	p, pr := requireSession(ctx)
	if pr != nil {
		if pr.Status == http.StatusUnauthorized {
			return api.ChangeOwnPassword401ApplicationProblemPlusJSONResponse(*pr), nil
		}
		return api.ChangeOwnPassword403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	if req.Body == nil {
		return api.ChangeOwnPassword400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	keys := []string{"user:" + strings.ToLower(p.Username), "ip:" + remoteIP(ctx).String()}
	now := s.auth.clock()
	if wait := s.auth.lim.blocked(now, keys...); wait > 0 {
		return api.ChangeOwnPassword429ApplicationProblemPlusJSONResponse{TooManyAttemptsApplicationProblemPlusJSONResponse: tooMany(wait)}, nil
	}
	u, err := s.store.UserByID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if ok, _ := settings.VerifyPassword(u.PasswordHash, req.Body.CurrentPassword); !ok {
		s.auth.lim.fail(now, keys...)
		return api.ChangeOwnPassword403ApplicationProblemPlusJSONResponse(newProblem(http.StatusForbidden, "bad_credentials", "Wrong current password", "", nil)), nil
	}
	s.auth.lim.succeed(keys...)
	hash, err := settings.HashPassword(req.Body.NewPassword)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetPassword(ctx, u.ID, hash, p.SessionHash); err != nil {
		return nil, err
	}
	s.endSessions(sessionEnd{Reason: endPasswordChanged, UserID: u.ID, Keep: p.SessionHash})
	slog.Info("admin password changed", "username", u.Username)
	return api.ChangeOwnPassword204Response{}, nil
}

// ---------------------------------------------------------------- users

// ListUsers implements GET /users.
func (s *Server) ListUsers(ctx context.Context, _ api.ListUsersRequestObject) (api.ListUsersResponseObject, error) {
	if _, pr := requireSession(ctx); pr != nil {
		return api.ListUsers403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	us, err := s.store.Users(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ListUsers200JSONResponse{Items: []api.User{}}
	for _, u := range us {
		out.Items = append(out.Items, userToAPI(u))
	}
	return out, nil
}

// CreateUser implements POST /users.
func (s *Server) CreateUser(ctx context.Context, req api.CreateUserRequestObject) (api.CreateUserResponseObject, error) {
	p, pr := requireSession(ctx)
	if pr != nil {
		return api.CreateUser403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	if req.Body == nil {
		return api.CreateUser400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	hash, err := settings.HashPassword(req.Body.Password)
	if err != nil {
		return nil, err
	}
	u, err := s.store.CreateUser(ctx, req.Body.Username, hash)
	if errors.Is(err, store.ErrUsernameTaken) {
		return api.CreateUser409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "username_taken", "Username taken",
			"Another account has this username (ignoring case).", nil)), nil
	}
	if err != nil {
		return nil, err
	}
	slog.Info("admin account created", "username", u.Username, "by", p.Username)
	return api.CreateUser201JSONResponse(userToAPI(u)), nil
}

// DeleteUser implements DELETE /users/{userId}.
func (s *Server) DeleteUser(ctx context.Context, req api.DeleteUserRequestObject) (api.DeleteUserResponseObject, error) {
	p, pr := requireSession(ctx)
	if pr != nil {
		return api.DeleteUser403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	err := s.store.DeleteUser(ctx, req.UserId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.DeleteUser404ApplicationProblemPlusJSONResponse(newProblem(http.StatusNotFound, "not_found", "No such account", "", nil)), nil
	case errors.Is(err, store.ErrLastUser):
		return api.DeleteUser409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "last_user", "The last account",
			"Create another account before deleting this one.", nil)), nil
	case err != nil:
		return nil, err
	}
	s.endSessions(sessionEnd{Reason: endAccountDeleted, UserID: req.UserId, Tokens: true})
	slog.Info("admin account deleted", "user_id", req.UserId, "by", p.Username)
	return api.DeleteUser204Response{}, nil
}

// SetUserPassword implements PUT /users/{userId}/password.
func (s *Server) SetUserPassword(ctx context.Context, req api.SetUserPasswordRequestObject) (api.SetUserPasswordResponseObject, error) {
	p, pr := requireSession(ctx)
	if pr != nil {
		return api.SetUserPassword403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	if req.Body == nil {
		return api.SetUserPassword400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	hash, err := settings.HashPassword(req.Body.Password)
	if err != nil {
		return nil, err
	}
	keep := ""
	if req.UserId == p.UserID {
		keep = p.SessionHash
	}
	if err := s.store.SetPassword(ctx, req.UserId, hash, keep); errors.Is(err, store.ErrNotFound) {
		return api.SetUserPassword404ApplicationProblemPlusJSONResponse(newProblem(http.StatusNotFound, "not_found", "No such account", "", nil)), nil
	} else if err != nil {
		return nil, err
	}
	s.endSessions(sessionEnd{Reason: endPasswordChanged, UserID: req.UserId, Keep: keep})
	slog.Info("admin password set", "user_id", req.UserId, "by", p.Username)
	return api.SetUserPassword204Response{}, nil
}

// ---------------------------------------------------------------- tokens

// ListTokens implements GET /tokens.
func (s *Server) ListTokens(ctx context.Context, _ api.ListTokensRequestObject) (api.ListTokensResponseObject, error) {
	if _, pr := requireSession(ctx); pr != nil {
		return api.ListTokens403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	ts, err := s.store.APITokens(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ListTokens200JSONResponse{Items: []api.ApiToken{}}
	for _, t := range ts {
		out.Items = append(out.Items, tokenToAPI(t))
	}
	return out, nil
}

// CreateToken implements POST /tokens.
func (s *Server) CreateToken(ctx context.Context, req api.CreateTokenRequestObject) (api.CreateTokenResponseObject, error) {
	p, pr := requireSession(ctx)
	if pr != nil {
		return api.CreateToken403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	if req.Body == nil {
		return api.CreateToken400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("A JSON body is required.")}, nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return api.CreateToken400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("name is required.")}, nil
	}
	if e := req.Body.ExpiresAt; e != nil && !e.After(s.auth.clock()) {
		return api.CreateToken400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: badRequest("expiresAt must be in the future.")}, nil
	}
	secret, hash := newAPIToken()
	t, err := s.store.CreateAPIToken(ctx, hash, name, p.UserID, req.Body.ExpiresAt)
	if err != nil {
		return nil, err
	}
	slog.Info("API token created", "token_id", t.ID, "name", t.Name, "by", p.Username)
	a := tokenToAPI(t)
	return api.CreateToken201JSONResponse{Id: a.Id, Name: a.Name, CreatedAt: a.CreatedAt, CreatedBy: a.CreatedBy,
		ExpiresAt: a.ExpiresAt, LastUsedAt: a.LastUsedAt, Secret: secret}, nil
}

// RevokeToken implements DELETE /tokens/{tokenId}.
func (s *Server) RevokeToken(ctx context.Context, req api.RevokeTokenRequestObject) (api.RevokeTokenResponseObject, error) {
	p, pr := requireSession(ctx)
	if pr != nil {
		return api.RevokeToken403ApplicationProblemPlusJSONResponse(*pr), nil
	}
	if err := s.store.DeleteAPIToken(ctx, req.TokenId); errors.Is(err, store.ErrNotFound) {
		return api.RevokeToken404ApplicationProblemPlusJSONResponse(newProblem(http.StatusNotFound, "not_found", "No such token", "", nil)), nil
	} else if err != nil {
		return nil, err
	}
	s.endSessions(sessionEnd{Reason: endTokenRevoked, Token: req.TokenId})
	slog.Info("API token revoked", "token_id", req.TokenId, "by", p.Username)
	return api.RevokeToken204Response{}, nil
}

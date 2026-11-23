package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

// The agent API (ADR 0025). Bearer tokens and the caller's address reach
// the strict handlers through the request context (WithRequestInfo).

const (
	sessionTokenTTL    = 5 * time.Minute
	deploymentTokenTTL = 24 * time.Hour
	checkInTTL         = time.Hour
)

type ctxKey int

const (
	ctxBearer ctxKey = iota
	ctxRemoteIP
	ctxAccept
	ctxLocalIP
)

// WithRequestInfo puts the bearer token and the caller's IP into the
// request context for the agent handlers.
func WithRequestInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			ctx = context.WithValue(ctx, ctxBearer, strings.TrimSpace(tok))
		}
		if acc := r.Header.Get("Accept"); acc != "" {
			ctx = context.WithValue(ctx, ctxAccept, acc)
		}
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			if a, err := netip.ParseAddr(host); err == nil {
				ctx = context.WithValue(ctx, ctxRemoteIP, a.Unmap())
			}
		}
		if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			if ap, err := netip.ParseAddrPort(la.String()); err == nil {
				ctx = context.WithValue(ctx, ctxLocalIP, ap.Addr().Unmap())
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// localAddr is the address the request came in on.
func localAddr(ctx context.Context) netip.Addr { a, _ := ctx.Value(ctxLocalIP).(netip.Addr); return a }

func bearer(ctx context.Context) string { s, _ := ctx.Value(ctxBearer).(string); return s }

func remoteIP(ctx context.Context) netip.Addr { a, _ := ctx.Value(ctxRemoteIP).(netip.Addr); return a }

func tokenHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, string) {
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	return tok, tokenHash(tok)
}

// agentState is the agent API's memory: which client checked in from where
// (unlock needs it) and failed unlock attempts.
type agentState struct {
	mu       sync.Mutex
	checkIns map[string]checkIn // client id → last check-in
	failures map[string]*limit  // "mac:…" / "ip:…"
	now      func() time.Time
	// bootPrompt replaces the settings in tests.
	bootPrompt func() settings.BootPrompt
	// httpEndpoints replaces the settings' HTTP content endpoints in tests.
	httpEndpoints func() []netip.AddrPort
	// touched is when each Linux deployment's activity was last recorded
	// (installer reports are throttled, ADR 0036).
	touched map[string]time.Time
}

type checkIn struct {
	ip  netip.Addr
	mac string
	at  time.Time
}

type limit struct {
	failures int
	until    time.Time
}

func (a *agentState) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *agentState) recordCheckIn(clientID, mac string, ip netip.Addr) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.checkIns == nil {
		a.checkIns = map[string]checkIn{}
	}
	now := a.clock()
	for id, c := range a.checkIns {
		if now.Sub(c.at) > checkInTTL {
			delete(a.checkIns, id)
		}
	}
	a.checkIns[clientID] = checkIn{ip: ip, mac: mac, at: now}
}

func (a *agentState) checkedIn(clientID string, ip netip.Addr) (checkIn, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.checkIns[clientID]
	return c, ok && c.ip == ip && a.clock().Sub(c.at) <= checkInTTL
}

// Unlock rate limit: 5 free failures per key, then a doubling back-off from
// 5 s up to 5 minutes.
const (
	freeFailures = 5
	maxBackoff   = 5 * time.Minute
)

// blocked returns how long until keys may try again (0: now).
func (a *agentState) blocked(keys ...string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	var wait time.Duration
	for _, k := range keys {
		if l := a.failures[k]; l != nil {
			wait = max(wait, l.until.Sub(a.clock()))
		}
	}
	return max(wait, 0)
}

func (a *agentState) fail(keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failures == nil {
		a.failures = map[string]*limit{}
	}
	for _, k := range keys {
		l := a.failures[k]
		if l == nil {
			l = &limit{}
			a.failures[k] = l
		}
		l.failures++
		if n := l.failures - freeFailures; n > 0 {
			d := 5 * time.Second << min(n-1, 10)
			l.until = a.clock().Add(min(d, maxBackoff))
		}
	}
}

func (a *agentState) succeed(keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range keys {
		delete(a.failures, k)
	}
}

func (s *Server) bootPrompt() settings.BootPrompt {
	if s.agent.bootPrompt != nil { // tests
		return s.agent.bootPrompt()
	}
	if s.settings == nil {
		return settings.BootPrompt{RequirePassword: true, TimeoutSeconds: 30, ArmedCountdownSeconds: 10}
	}
	cfg, _ := s.settings.Current()
	return cfg.BootPrompt
}

func (s *Server) deploymentEvent(d store.Deployment) {
	s.events.Publish(events.Event{Type: events.DeploymentUpdated, ResourceID: d.ID,
		Data: map[string]any{"clientId": d.ClientID, "status": d.Status, "stage": d.Stage, "percent": d.Percent}})
}

func agentBadRequest(detail string) api.ProblemApplicationProblemPlusJSONResponse {
	return badRequest(detail)
}

func agentUnauthorized() api.Problem {
	return newProblem(http.StatusUnauthorized, "invalid_token", "Invalid token", "The agent token is missing, expired or for a deployment that isn't running.", nil)
}

// ---------------------------------------------------------------- check-in

// AgentCheckIn implements POST /agent/check-in.
func (s *Server) AgentCheckIn(ctx context.Context, req api.AgentCheckInRequestObject) (api.AgentCheckInResponseObject, error) {
	b := req.Body
	if b == nil || len(b.Macs) == 0 {
		return api.AgentCheckIn400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: agentBadRequest("macs is required.")}, nil
	}
	macs := make([]string, 0, len(b.Macs))
	for _, m := range b.Macs {
		n, err := store.NormalizeMAC(m)
		if err != nil {
			return api.AgentCheckIn400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: agentBadRequest("Invalid MAC " + m + ".")}, nil
		}
		macs = append(macs, n)
	}
	ip := remoteIP(ctx)
	log := slog.With("svc", "agent", "ip", ip.String(), "macs", macs, "agent_version", ptrOr(b.AgentVersion, ""))

	// The client: the first MAC that has one, else a new discovered client.
	known, err := s.store.ClientsByMAC(ctx, macs)
	if err != nil {
		return nil, err
	}
	var c store.Client
	found := false
	for _, m := range macs {
		if kc, ok := known[m]; ok {
			c, found = kc, true
			break
		}
	}
	// Create the client, or fill in hardware details it lacks (a client
	// first seen by DHCP has no serial): computer name patterns use them.
	mac := macs[0]
	if found {
		mac = c.MAC
	}
	ipStr := ip.String()
	serial := b.Serial
	if serial != nil && unattend.PlaceholderSerial(*serial) {
		serial = nil // "Undefined" and the like: no serial at all
	}
	sg := store.Sighting{MAC: mac, SMBIOSUUID: b.SmbiosUuid, Serial: serial}
	if ip.IsValid() {
		sg.IP = &ipStr
	}
	c, created, err := s.store.RecordSighting(ctx, sg)
	if err != nil {
		return nil, err
	}
	if created {
		s.clientEvent(c.ID, c.MAC, "created")
	}
	s.agent.recordCheckIn(c.ID, c.MAC, ip)

	// The reboot rule (ADR 0025): a running deployment restarted.
	if d, err := s.store.ActiveDeployment(ctx, c.ID); err == nil && d.Status == store.StatusRunning {
		if d, err = s.store.FinishDeployment(ctx, d.ID, store.StatusFailed, "restarted: the machine restarted during the deployment"); err == nil {
			log.Warn("running deployment failed: the client restarted", "deployment_id", d.ID)
			s.deploymentEvent(d)
		}
	} else if err == nil && d.Status == store.StatusPending {
		if c.LastIP != nil && ip.IsValid() && *c.LastIP != ip.String() {
			log.Warn("armed deployment not handed over: check-in from a different address than DHCP gave the client",
				"deployment_id", d.ID, "dhcp_ip", *c.LastIP)
		} else if snapshotIsLinux(d.ProfileSnapshot) {
			log.Warn("armed deployment not handed over: it's a Linux one, started by the client's iPXE script", "deployment_id", d.ID)
		} else if out, ok, err := s.startArmed(ctx, c, d); err != nil {
			return nil, err
		} else if ok {
			log.Info("armed deployment started", "deployment_id", d.ID, "client_id", c.ID)
			return api.AgentCheckIn200JSONResponse(out), nil
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	bp := s.bootPrompt()
	out := api.AgentCheckInResult{State: api.AgentCheckInResultStateLocked, ClientId: c.ID}
	out.Prompt = &struct {
		PasswordConfigured bool `json:"passwordConfigured"`
		RequirePassword    bool `json:"requirePassword"`
		TimeoutSeconds     int  `json:"timeoutSeconds"`
	}{PasswordConfigured: bp.PasswordHash != "", RequirePassword: bp.RequirePassword, TimeoutSeconds: bp.TimeoutSeconds}
	log.Info("agent checked in", "client_id", c.ID, "state", "locked")
	return api.AgentCheckIn200JSONResponse(out), nil
}

// startArmed consumes an armed deployment and returns the armed result.
func (s *Server) startArmed(ctx context.Context, c store.Client, d store.Deployment) (api.AgentCheckInResult, bool, error) {
	d, err := s.store.StartDeployment(ctx, d.ID)
	if errors.Is(err, store.ErrBadTransition) {
		return api.AgentCheckInResult{}, false, nil // someone else consumed it
	}
	if err != nil {
		return api.AgentCheckInResult{}, false, err
	}
	tok, err := s.startPlan(ctx, d)
	if err != nil {
		return api.AgentCheckInResult{}, false, err
	}
	var snap struct {
		Name string `json:"name"`
	}
	json.Unmarshal(d.ProfileSnapshot, &snap)
	out := api.AgentCheckInResult{State: api.AgentCheckInResultStateArmed, ClientId: c.ID}
	out.Armed = &struct {
		CountdownSeconds int       `json:"countdownSeconds"`
		DeploymentId     string    `json:"deploymentId"`
		ExpiresAt        time.Time `json:"expiresAt"`
		ProfileName      string    `json:"profileName"`
		Token            string    `json:"token"`
	}{CountdownSeconds: s.bootPrompt().ArmedCountdownSeconds, DeploymentId: d.ID, ExpiresAt: tok.ExpiresAt, ProfileName: snap.Name, Token: tok.Token}
	return out, true, nil
}

// startPlan builds a running deployment's plan and issues its token. If the
// plan can't be built the deployment fails.
func (s *Server) startPlan(ctx context.Context, d store.Deployment) (api.AgentToken, error) {
	plan, files, err := s.buildPlan(ctx, d)
	if err == nil {
		var b []byte
		if b, err = json.Marshal(plan); err == nil {
			err = s.store.SetDeploymentPlan(ctx, d.ID, b, files)
		}
	}
	if err != nil {
		if fd, ferr := s.store.FinishDeployment(context.WithoutCancel(ctx), d.ID, store.StatusFailed, "plan: "+err.Error()); ferr == nil {
			s.deploymentEvent(fd)
		}
		return api.AgentToken{}, err
	}
	tok, hash := newToken()
	exp := time.Now().Add(deploymentTokenTTL).UTC()
	if err := s.store.CreateAgentToken(ctx, store.AgentToken{Hash: hash, Kind: store.TokenDeployment, ClientID: d.ClientID,
		DeploymentID: &d.ID, ExpiresAt: exp}); err != nil {
		return api.AgentToken{}, err
	}
	s.deploymentEvent(d)
	return api.AgentToken{Token: tok, DeploymentId: d.ID, ExpiresAt: exp}, nil
}

// ---------------------------------------------------------------- unlock

// AgentUnlock implements POST /agent/unlock.
func (s *Server) AgentUnlock(ctx context.Context, req api.AgentUnlockRequestObject) (api.AgentUnlockResponseObject, error) {
	b := req.Body
	if b == nil || b.ClientId == "" {
		return api.AgentUnlock400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: agentBadRequest("clientId is required.")}, nil
	}
	ip := remoteIP(ctx)
	ci, ok := s.agent.checkedIn(b.ClientId, ip)
	if !ok {
		return api.AgentUnlock404ApplicationProblemPlusJSONResponse(newProblem(http.StatusNotFound, "unknown_client", "Unknown client",
			"Check in from this address first.", nil)), nil
	}
	keys := []string{"mac:" + ci.mac, "ip:" + ip.String()}
	bp := s.bootPrompt()
	if bp.RequirePassword {
		if bp.PasswordHash == "" {
			return api.AgentUnlock403ApplicationProblemPlusJSONResponse(newProblem(http.StatusForbidden, "password_not_configured", "No password configured",
				"No boot prompt password is set: only deployments armed through the API can run.", nil)), nil
		}
		if wait := s.agent.blocked(keys...); wait > 0 {
			secs := int(wait.Round(time.Second) / time.Second)
			p := newProblem(http.StatusTooManyRequests, "too_many_attempts", "Too many attempts", fmt.Sprintf("Try again in %d s.", secs), nil)
			return agentUnlock429{p: p, retryAfter: secs}, nil
		}
		ok, err := settings.VerifyPassword(bp.PasswordHash, ptrOr(b.Password, ""))
		if err != nil {
			return nil, err
		}
		if !ok {
			s.agent.fail(keys...)
			slog.Warn("boot prompt: wrong password", "svc", "agent", "client_id", b.ClientId, "ip", ip.String())
			return api.AgentUnlock401ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnauthorized, "invalid_password", "Wrong password", "", nil)), nil
		}
		s.agent.succeed(keys...)
	}
	tok, hash := newToken()
	exp := time.Now().Add(sessionTokenTTL).UTC()
	if err := s.store.CreateAgentToken(ctx, store.AgentToken{Hash: hash, Kind: store.TokenSession, ClientID: b.ClientId, ExpiresAt: exp}); err != nil {
		return nil, err
	}
	profiles, _, err := s.store.ListProfiles(ctx, 200, "")
	if err != nil {
		return nil, err
	}
	out := api.AgentUnlockResult{SessionToken: tok, ExpiresAt: exp}
	for _, p := range profiles {
		if p.Kind == store.ProfileLinux {
			continue // the iPXE menu offers those (ADR 0034)
		}
		item := struct {
			Description *string `json:"description,omitempty"`
			Id          string  `json:"id"`
			Name        string  `json:"name"`
		}{Id: p.ID, Name: p.Name}
		if p.Description != "" {
			d := p.Description
			item.Description = &d
		}
		out.Profiles = append(out.Profiles, item)
	}
	if c, err := s.store.Client(ctx, b.ClientId); err == nil && c.ProfileID != nil {
		out.DefaultProfileId = c.ProfileID
	} else if bp.DefaultProfileID != "" {
		def := bp.DefaultProfileID
		out.DefaultProfileId = &def
	}
	slog.Info("boot prompt unlocked", "svc", "agent", "client_id", b.ClientId, "ip", ip.String())
	return api.AgentUnlock200JSONResponse(out), nil
}

// agentUnlock429 adds Retry-After to the 429 problem.
type agentUnlock429 struct {
	p          api.Problem
	retryAfter int
}

func (r agentUnlock429) VisitAgentUnlockResponse(w http.ResponseWriter) error {
	w.Header().Set("Retry-After", fmt.Sprint(r.retryAfter))
	return api.AgentUnlock429ApplicationProblemPlusJSONResponse(r.p).VisitAgentUnlockResponse(w)
}

// ---------------------------------------------------------------- start

// AgentStartDeployment implements POST /agent/deployments.
func (s *Server) AgentStartDeployment(ctx context.Context, req api.AgentStartDeploymentRequestObject) (api.AgentStartDeploymentResponseObject, error) {
	if req.Body == nil || req.Body.ProfileId == "" {
		return api.AgentStartDeployment400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: agentBadRequest("profileId is required.")}, nil
	}
	sess, err := s.store.UseSessionToken(ctx, tokenHash(bearer(ctx)))
	if errors.Is(err, store.ErrTokenInvalid) {
		return api.AgentStartDeployment401ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnauthorized, "invalid_session",
			"Invalid session", "The boot prompt session is missing, expired or used; unlock again.", nil)), nil
	}
	if err != nil {
		return nil, err
	}
	p, err := s.store.Profile(ctx, req.Body.ProfileId)
	if errors.Is(err, store.ErrNotFound) {
		return api.AgentStartDeployment404ApplicationProblemPlusJSONResponse(newProblem(http.StatusNotFound, "profile_not_found", "Profile not found", "", nil)), nil
	}
	if err != nil {
		return nil, err
	}
	if p.Kind == store.ProfileLinux {
		return api.AgentStartDeployment422ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnprocessableEntity, "not_for_kind",
			"Linux profile", "Profile "+p.Name+" is a Linux profile: it is started from the iPXE menu, not WinPE.", nil)), nil
	}
	c, err := s.store.Client(ctx, sess.ClientID)
	if err != nil {
		return nil, err
	}
	nd, prob, err := s.prepareDeployment(ctx, c, p, store.OriginBootPrompt)
	if err != nil {
		return nil, err
	}
	if prob != nil {
		return api.AgentStartDeployment422ApplicationProblemPlusJSONResponse(*prob), nil
	}
	// Supersede whatever is active (ADR 0013).
	if d, err := s.store.ActiveDeployment(ctx, sess.ClientID); err == nil {
		status, msg := store.StatusFailed, "superseded: a deployment was started from the boot prompt"
		if d.Status == store.StatusPending {
			status, msg = store.StatusCancelled, "superseded: a deployment was started from the boot prompt"
		}
		if d, err := s.store.FinishDeployment(ctx, d.ID, status, msg); err == nil {
			s.deploymentEvent(d)
		}
	}
	d, err := s.store.CreateDeployment(ctx, nd)
	if err != nil {
		return nil, err
	}
	tok, err := s.startPlan(ctx, d)
	if err != nil {
		return api.AgentStartDeployment422ApplicationProblemPlusJSONResponse(newProblem(http.StatusUnprocessableEntity, "plan_failed",
			"Deployment can't start", err.Error(), nil)), nil
	}
	slog.Info("deployment started from the boot prompt", "svc", "agent", "deployment_id", d.ID, "client_id", d.ClientID, "profile", p.Name)
	return api.AgentStartDeployment201JSONResponse(tok), nil
}

// profileSnapshot is the profile as a ProfileInput document.
func profileSnapshot(p store.Profile) (json.RawMessage, error) {
	out, err := profileToAPI(p)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"id", "etag", "createdAt", "updatedAt"} {
		delete(m, k)
	}
	return json.Marshal(m)
}

// ---------------------------------------------------------------- plan

// buildPlan builds a deployment's plan from its profile snapshot (ADR 0025)
// and lists the blobs it may download.
func (s *Server) buildPlan(ctx context.Context, d store.Deployment) (api.AgentDeploymentPlan, []string, error) {
	var snap api.ProfileInput
	if err := json.Unmarshal(d.ProfileSnapshot, &snap); err != nil {
		return api.AgentDeploymentPlan{}, nil, fmt.Errorf("profile snapshot: %w", err)
	}
	if snap.Kind != nil && *snap.Kind == api.ProfileKindLinux {
		return api.AgentDeploymentPlan{}, nil, fmt.Errorf("profile %q is a Linux profile: the WinPE agent can't deploy it", snap.Name)
	}
	if snap.InstallMethod == nil || *snap.InstallMethod != api.InstallMethodAgent || snap.EditionIndex == nil || snap.BootImageId == nil {
		return api.AgentDeploymentPlan{}, nil, fmt.Errorf("install method %v isn't supported yet", snap.InstallMethod)
	}
	im, err := s.store.Image(ctx, snap.InstallImageId)
	if err != nil {
		return api.AgentDeploymentPlan{}, nil, fmt.Errorf("install image %s: %w", snap.InstallImageId, err)
	}
	if im.TreeID == nil {
		return api.AgentDeploymentPlan{}, nil, fmt.Errorf("install image %q isn't ready", im.Name)
	}
	var files []string
	var image *store.TreeEntry
	for _, name := range []string{"sources/install.wim", "sources/install.esd"} {
		if e, err := s.store.LookupTreeEntry(ctx, *im.TreeID, cas.Fold(name)); err == nil {
			image = &e
			break
		}
	}
	if image == nil {
		return api.AgentDeploymentPlan{}, nil, fmt.Errorf("image %q has no sources/install.wim or install.esd (split images aren't supported)", im.Name)
	}
	files = append(files, image.SHA256)
	size := int(image.Size)

	plan := api.AgentDeploymentPlan{
		DeploymentId: d.ID, InstallMethod: api.InstallMethodAgent, EditionIndex: *snap.EditionIndex,
		BootOrderAfterDeploy: api.AgentDeploymentPlanBootOrderAfterDeploy("disk-first"),
		BootManager:          api.AgentDeploymentPlanBootManager("windows-pca-2011"),
	}
	// ?name= tells the agent the file's name (DISM goes by .wim or .esd).
	plan.Image.ContentUrl = agentFileURL(image.SHA256) + "?name=" + path.Base(image.Path)
	plan.Image.Sha256, plan.Image.SizeBytes = image.SHA256, &size
	if snap.BootOrderAfterDeploy != nil {
		plan.BootOrderAfterDeploy = api.AgentDeploymentPlanBootOrderAfterDeploy(*snap.BootOrderAfterDeploy)
	}
	if bi, err := s.store.BootImage(ctx, *snap.BootImageId); err == nil && bi.SelectedBootManager != nil {
		var bm struct {
			Signer string `json:"signer"`
		}
		if json.Unmarshal(bi.SelectedBootManager, &bm) == nil && bm.Signer == "windows-uefi-ca-2023" {
			plan.BootManager = api.AgentDeploymentPlanBootManager(bm.Signer)
		}
	}
	layout := api.AgentDeploymentPlanDiskLayout("uefi-gpt-default")
	plan.Disk = &struct {
		Layout          *api.AgentDeploymentPlanDiskLayout `json:"layout,omitempty"`
		TargetDiskIndex *int                               `json:"targetDiskIndex"`
	}{Layout: &layout}

	// OS drivers: every file of each enabled INF's folder, one numbered
	// folder per (package, INF folder).
	if snap.OsDriverGroupIds != nil && len(*snap.OsDriverGroupIds) > 0 {
		drivers, err := s.store.BootDrivers(ctx, *snap.OsDriverGroupIds)
		if err != nil {
			return api.AgentDeploymentPlan{}, nil, err
		}
		type osFile = struct {
			ContentUrl string `json:"contentUrl"`
			Path       string `json:"path"`
			Sha256     string `json:"sha256"`
			SizeBytes  *int   `json:"sizeBytes,omitempty"`
		}
		var out []osFile
		dirs := map[string]int{}
		trees := map[string][]store.TreeEntry{}
		for _, drv := range drivers {
			dir := path.Dir(drv.Path)
			key := drv.PackageID + "|" + dir
			if _, ok := dirs[key]; ok {
				continue
			}
			n := len(dirs) + 1
			dirs[key] = n
			es, ok := trees[drv.TreeID]
			if !ok {
				if es, err = s.store.TreeEntries(ctx, drv.TreeID); err != nil {
					return api.AgentDeploymentPlan{}, nil, err
				}
				trees[drv.TreeID] = es
			}
			for _, e := range es {
				rel, ok := underDir(e.Path, dir)
				if !ok {
					continue
				}
				sz := int(e.Size)
				out = append(out, osFile{ContentUrl: agentFileURL(e.SHA256), Path: fmt.Sprintf("%d/%s", n, rel), Sha256: e.SHA256, SizeBytes: &sz})
				files = append(files, e.SHA256)
			}
		}
		plan.OsDrivers = &out
	}

	// The built-in unattend and identity files (ADR 0026).
	u := apiBase + "/agent/unattend"
	dest := api.AgentDeploymentPlanUnattendDestinationPanther
	plan.UnattendUrl, plan.UnattendDestination = &u, &dest
	return plan, files, nil
}

type secretValue struct {
	kind  string
	value []byte
}

// deploymentSecretValues decrypts a deployment's secret copies, by secret id.
func (s *Server) deploymentSecretValues(ctx context.Context, id string) (map[string]secretValue, error) {
	copies, err := s.store.DeploymentSecrets(ctx, id)
	if err != nil || len(copies) == 0 {
		return nil, err
	}
	if s.vault == nil {
		return nil, errors.New("secrets are not available")
	}
	r, err := s.vault.Keys().Ring(false)
	if err != nil {
		return nil, fmt.Errorf("secrets key: %w", err)
	}
	out := map[string]secretValue{}
	for _, c := range copies {
		if c.KeyFingerprint != r.Fingerprint() {
			return nil, fmt.Errorf("secret %s was sealed with another key; import the key export", c.SecretID)
		}
		p, err := r.Open(c.SecretID, c.Kind, c.ValueVersion, c.Value)
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", c.SecretID, err)
		}
		out[c.SecretID] = secretValue{c.Kind, p}
	}
	return out, nil
}

// AgentGetUnattend implements GET /agent/unattend: rendered on each
// request, never stored (ADR 0026).
func (s *Server) AgentGetUnattend(ctx context.Context, _ api.AgentGetUnattendRequestObject) (api.AgentGetUnattendResponseObject, error) {
	t, ok, err := s.deploymentToken(ctx, false)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.AgentGetUnattend401ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(agentUnauthorized())}, nil
	}
	d, err := s.store.Deployment(ctx, *t.DeploymentID)
	if err != nil {
		return nil, err
	}
	p, err := unattend.Parse(d.ProfileSnapshot)
	if err != nil {
		return nil, err
	}
	values, err := s.deploymentSecretValues(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	tpl, err := deploymentTemplate(d)
	if err != nil {
		return nil, err
	}
	c, err := s.store.Client(ctx, d.ClientID)
	if err != nil {
		return nil, err
	}
	in := unattend.Input{Arch: s.unattendArch(ctx, d), InstallMethod: snapshotInstallMethod(d.ProfileSnapshot), ComputerName: d.ComputerName,
		Secrets: map[string][]byte{}, Client: clientData(c), ProfileID: d.ProfileID, DeploymentID: d.ID, Variables: d.Variables, Template: tpl}
	for id, v := range values {
		in.Secrets[id] = v.value
		if v.kind == "odj-blob" {
			in.ODJBlob = v.value
		}
	}
	b, err := unattend.Render(p, in)
	if err != nil {
		return nil, err
	}
	return api.AgentGetUnattend200ApplicationxmlResponse{Body: bytes.NewReader(b), ContentLength: int64(len(b))}, nil
}

// unattendArch is the deployment's install image architecture as unattend
// names it.
func (s *Server) unattendArch(ctx context.Context, d store.Deployment) string {
	var snap struct {
		InstallImageID string `json:"installImageId"`
	}
	json.Unmarshal(d.ProfileSnapshot, &snap)
	return s.imageArch(ctx, snap.InstallImageID)
}

// imageArch is an install image's architecture as unattend names it:
// amd64 or arm64.
func (s *Server) imageArch(ctx context.Context, imageID string) string {
	if im, err := s.store.Image(ctx, imageID); err == nil {
		var det struct {
			Architecture string `json:"architecture"`
		}
		if json.Unmarshal(im.Details, &det) == nil && det.Architecture == "arm64" {
			return "arm64"
		}
	}
	return "amd64"
}

func agentFileURL(sha string) string { return apiBase + "/agent/files/" + sha }

// underDir returns p relative to dir ("." is the root), case-insensitively.
func underDir(p, dir string) (string, bool) {
	if dir == "." {
		return p, true
	}
	if len(p) > len(dir)+1 && strings.EqualFold(p[:len(dir)], dir) && p[len(dir)] == '/' {
		return p[len(dir)+1:], true
	}
	return "", false
}

// ---------------------------------------------------------------- deployment calls

// cancelGrace is how long a cancelled deployment's token still works for
// the agent's logs and acknowledgement (ADR 0026).
const cancelGrace = 30 * time.Minute

// deploymentToken checks the bearer token and records the agent's request
// (the inactivity timeout). allowCancelled also accepts a recently
// cancelled deployment's token.
func (s *Server) deploymentToken(ctx context.Context, allowCancelled bool) (store.AgentToken, bool, error) {
	var grace time.Duration
	if allowCancelled {
		grace = cancelGrace
	}
	t, err := s.store.DeploymentToken(ctx, tokenHash(bearer(ctx)), grace)
	if errors.Is(err, store.ErrTokenInvalid) {
		return t, false, nil
	}
	if err == nil {
		if terr := s.store.TouchDeployment(ctx, *t.DeploymentID); terr != nil {
			slog.Warn("record agent activity", "svc", "agent", "deployment_id", *t.DeploymentID, "err", terr)
		}
	}
	return t, err == nil, err
}

// AgentGetDeployment implements GET /agent/deployment.
func (s *Server) AgentGetDeployment(ctx context.Context, _ api.AgentGetDeploymentRequestObject) (api.AgentGetDeploymentResponseObject, error) {
	t, ok, err := s.deploymentToken(ctx, false)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.AgentGetDeployment401ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(agentUnauthorized())}, nil
	}
	raw, err := s.store.DeploymentPlan(ctx, *t.DeploymentID)
	if err != nil {
		return nil, err
	}
	var plan api.AgentDeploymentPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	return api.AgentGetDeployment200JSONResponse(plan), nil
}

// AgentReportProgress implements POST /agent/progress.
func (s *Server) AgentReportProgress(ctx context.Context, req api.AgentReportProgressRequestObject) (api.AgentReportProgressResponseObject, error) {
	t, ok, err := s.deploymentToken(ctx, true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.AgentReportProgress401ApplicationProblemPlusJSONResponse(agentUnauthorized()), nil
	}
	if d, err := s.store.Deployment(ctx, *t.DeploymentID); err == nil && d.Status == store.StatusCancelled {
		return api.AgentReportProgress409ApplicationProblemPlusJSONResponse(newProblem(http.StatusConflict, "deployment_cancelled",
			"Deployment cancelled", "Finish the command in progress, stop, upload the logs and call complete.", nil)), nil
	}
	if req.Body == nil {
		return api.AgentReportProgress400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: agentBadRequest("A JSON body is required.")}, nil
	}
	pct := 0
	if req.Body.Percent != nil {
		pct = *req.Body.Percent
	}
	if err := s.store.ReportDeploymentProgress(ctx, *t.DeploymentID, string(req.Body.Stage), pct, ptrOr(req.Body.Message, "")); err != nil {
		if errors.Is(err, store.ErrBadTransition) {
			return api.AgentReportProgress401ApplicationProblemPlusJSONResponse(agentUnauthorized()), nil
		}
		return nil, err
	}
	if d, err := s.store.Deployment(ctx, *t.DeploymentID); err == nil {
		s.deploymentEvent(d)
	}
	return api.AgentReportProgress204Response{}, nil
}

// AgentUploadLogs implements POST /agent/logs.
func (s *Server) AgentUploadLogs(ctx context.Context, req api.AgentUploadLogsRequestObject) (api.AgentUploadLogsResponseObject, error) {
	t, ok, err := s.deploymentToken(ctx, true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.AgentUploadLogs401ApplicationProblemPlusJSONResponse(agentUnauthorized()), nil
	}
	if req.Body == nil {
		return api.AgentUploadLogs400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: agentBadRequest("A JSON body is required.")}, nil
	}
	entries := make([]store.LogEntry, len(req.Body.Entries))
	for i, e := range req.Body.Entries {
		entries[i] = store.LogEntry{At: e.At, Level: string(e.Level), Message: e.Message, Source: e.Source}
	}
	if err := s.store.AppendDeploymentLogs(ctx, *t.DeploymentID, entries); err != nil {
		return nil, err
	}
	return api.AgentUploadLogs204Response{}, nil
}

// AgentComplete implements POST /agent/complete. Idempotent: a repeat with
// the same result after the deployment finished is accepted.
func (s *Server) AgentComplete(ctx context.Context, req api.AgentCompleteRequestObject) (api.AgentCompleteResponseObject, error) {
	if req.Body == nil {
		return api.AgentComplete400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: agentBadRequest("A JSON body is required.")}, nil
	}
	t, err := s.store.AgentTokenAny(ctx, tokenHash(bearer(ctx)))
	if errors.Is(err, store.ErrTokenInvalid) || (err == nil && t.DeploymentID == nil) {
		return api.AgentComplete401ApplicationProblemPlusJSONResponse(agentUnauthorized()), nil
	}
	if err != nil {
		return nil, err
	}
	// A cancelled deployment: this is the agent's acknowledgement (ADR 0026).
	if cur, err := s.store.Deployment(ctx, *t.DeploymentID); err == nil && cur.Status == store.StatusCancelled {
		if cur.FinishedAt == nil || time.Since(*cur.FinishedAt) > cancelGrace {
			return api.AgentComplete401ApplicationProblemPlusJSONResponse(agentUnauthorized()), nil
		}
		if err := s.store.RevokeAgentToken(ctx, t.Hash); err != nil {
			return nil, err
		}
		slog.Info("agent stopped the cancelled deployment", "svc", "agent", "deployment_id", cur.ID, "message", ptrOr(req.Body.Message, ""))
		return api.AgentComplete204Response{}, nil
	}
	status := store.StatusFailed
	if req.Body.Result == api.AgentCompleteJSONBodyResultSucceeded {
		status = store.StatusSucceeded
	}
	d, err := s.store.FinishDeployment(ctx, *t.DeploymentID, status, ptrOr(req.Body.Message, ""))
	if errors.Is(err, store.ErrBadTransition) {
		return api.AgentComplete401ApplicationProblemPlusJSONResponse(agentUnauthorized()), nil
	}
	if err != nil {
		return nil, err
	}
	slog.Info("deployment finished", "svc", "agent", "deployment_id", d.ID, "status", d.Status, "message", d.Message)
	s.deploymentEvent(d)
	return api.AgentComplete204Response{}, nil
}

// AgentFiles serves GET/HEAD /api/v1/agent/files/{sha256} outside the
// strict handler (streamed, with Range): only blobs the deployment's plan
// lists, to its token.
func (s *Server) AgentFiles(w http.ResponseWriter, r *http.Request) {
	sha := strings.TrimPrefix(r.URL.Path, apiBase+"/agent/files/")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "Only GET and HEAD.")
		return
	}
	ctx := r.Context()
	t, ok, err := s.deploymentToken(ctx, false)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal error", "")
		return
	}
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "invalid_token", "Invalid token", "The agent token is missing, expired or for a deployment that isn't running.")
		return
	}
	allowed, err := s.store.DeploymentFileAllowed(ctx, *t.DeploymentID, sha)
	if err != nil || !allowed || s.content == nil {
		writeProblem(w, http.StatusNotFound, "not_found", "Not found", "This deployment's plan doesn't list that file.")
		return
	}
	p, err := s.content.BlobPath(sha)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not_found", "Not found", "")
		return
	}
	http.ServeFile(w, r, p) // Range, HEAD, If-Range
}

func ptrOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/autoinstall"
	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
	"github.com/michaelmadell/pxe-netdeploy/internal/ipxe"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// The iPXE side of the gate for Linux profiles (ADR 0034). Every answer is
// an iPXE script, failures included: iPXE shows nothing of an HTTP error
// body, and a script can say why and fall through to the disk.

// ipxeTokenTTL is how long the profile menu's token lasts.
const ipxeTokenTTL = 10 * time.Minute

// linuxPrefix is where content listeners serve Linux images (content.LinuxPrefix).
const linuxPrefix = "/content/linux/"

type ipxeScript string

func (s ipxeScript) VisitAgentIpxeUnlockResponse(w http.ResponseWriter) error { return s.write(w) }
func (s ipxeScript) VisitAgentIpxeStartResponse(w http.ResponseWriter) error  { return s.write(w) }

func (s ipxeScript) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(s))
	return err
}

// ipxeArch checks the posted architecture.
func ipxeArch(a api.Architecture) (string, bool) {
	switch a {
	case api.ArchitectureX64, api.ArchitectureArm64:
		return string(a), true
	}
	return "", false
}

// AgentIpxeUnlock implements POST /agent/ipxe/unlock: the password, then
// the Linux profile menu.
func (s *Server) AgentIpxeUnlock(ctx context.Context, req api.AgentIpxeUnlockRequestObject) (api.AgentIpxeUnlockResponseObject, error) {
	b := req.Body
	if b == nil {
		return ipxeScript(ipxe.Failure("x64", "bad request", false)), nil
	}
	arch, ok := ipxeArch(b.Arch)
	if !ok {
		return ipxeScript(ipxe.Failure("x64", "unknown architecture "+string(b.Arch), false)), nil
	}
	mac, err := store.NormalizeMAC(b.Mac)
	if err != nil {
		return ipxeScript(ipxe.Failure(arch, "bad MAC address "+b.Mac, false)), nil
	}
	ip := remoteIP(ctx)
	log := slog.With("svc", "agent", "via", "ipxe", "mac", mac, "ip", ip.String())
	keys := []string{"mac:" + mac, "ip:" + ip.String()}
	bp := s.bootPrompt()
	if bp.RequirePassword {
		if bp.PasswordHash == "" {
			return ipxeScript(ipxe.Failure(arch, "no boot prompt password is set on this server: only deployments armed there can run.", false)), nil
		}
		if wait := s.agent.blocked(keys...); wait > 0 {
			return ipxeScript(ipxe.Failure(arch, fmt.Sprintf("too many wrong passwords: try again in %d s.", int(wait.Round(time.Second)/time.Second)), true)), nil
		}
		ok, err := settings.VerifyPassword(bp.PasswordHash, ptrOr(b.Password, ""))
		if err != nil {
			return nil, err
		}
		if !ok {
			s.agent.fail(keys...)
			log.Warn("boot prompt: wrong password")
			return ipxeScript(ipxe.Failure(arch, "wrong password.", true)), nil
		}
		s.agent.succeed(keys...)
	}

	c, err := s.ipxeClient(ctx, mac, ip)
	if err != nil {
		return nil, err
	}
	profiles, _, err := s.store.ListProfiles(ctx, 200, "")
	if err != nil {
		return nil, err
	}
	var items []ipxe.MenuItem
	for _, p := range profiles {
		if p.Kind != store.ProfileLinux || !s.linuxProfileBootable(ctx, p, arch) {
			continue
		}
		items = append(items, ipxe.MenuItem{ID: p.ID, Name: p.Name, Description: p.Description})
	}
	if len(items) == 0 {
		return ipxeScript(ipxe.Failure(arch, "no Linux profile can be deployed to this machine ("+arch+").", false)), nil
	}
	def := bp.DefaultProfileID
	if c.ProfileID != nil {
		def = *c.ProfileID
	}
	tok, hash := newToken()
	if err := s.store.CreateAgentToken(ctx, store.AgentToken{Hash: hash, Kind: store.TokenSession, ClientID: c.ID,
		ExpiresAt: time.Now().Add(ipxeTokenTTL).UTC()}); err != nil {
		return nil, err
	}
	log.Info("boot prompt unlocked", "client_id", c.ID, "profiles", len(items))
	return ipxeScript(ipxe.LinuxMenu(arch, items, def, tok)), nil
}

// AgentIpxeStart implements POST /agent/ipxe/start: an armed deployment, or
// the profile chosen from the menu; the answer is the boot commands.
func (s *Server) AgentIpxeStart(ctx context.Context, req api.AgentIpxeStartRequestObject) (api.AgentIpxeStartResponseObject, error) {
	b := req.Body
	if b == nil {
		return ipxeScript(ipxe.Failure("x64", "bad request", false)), nil
	}
	arch, ok := ipxeArch(b.Arch)
	if !ok {
		return ipxeScript(ipxe.Failure("x64", "unknown architecture "+string(b.Arch), false)), nil
	}
	mac, err := store.NormalizeMAC(b.Mac)
	if err != nil {
		return ipxeScript(ipxe.Failure(arch, "bad MAC address "+b.Mac, false)), nil
	}
	ip := remoteIP(ctx)
	log := slog.With("svc", "agent", "via", "ipxe", "mac", mac, "ip", ip.String())
	c, err := s.ipxeClient(ctx, mac, ip)
	if err != nil {
		return nil, err
	}

	var d store.Deployment
	if ptrOr(b.Armed, false) {
		pending, err := s.store.ActiveDeployment(ctx, c.ID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && pending.Status != store.StatusPending) {
			return ipxeScript(ipxe.Failure(arch, "no deployment is armed for this machine any more.", true)), nil
		}
		if err != nil {
			return nil, err
		}
		if !snapshotIsLinux(pending.ProfileSnapshot) {
			return ipxeScript(ipxe.Failure(arch, "the armed deployment isn't a Linux one.", false)), nil
		}
		d, err = s.store.StartDeployment(ctx, pending.ID)
		if errors.Is(err, store.ErrBadTransition) {
			return ipxeScript(ipxe.Failure(arch, "the armed deployment was already started or cancelled.", true)), nil
		}
		if err != nil {
			return nil, err
		}
		s.deploymentEvent(d)
		log.Info("armed deployment started", "deployment_id", d.ID, "client_id", c.ID)
	} else {
		if b.UnlockToken == nil || b.ProfileId == nil {
			return ipxeScript(ipxe.Failure(arch, "profileId and unlockToken are required.", false)), nil
		}
		sess, err := s.store.UseSessionToken(ctx, tokenHash(*b.UnlockToken))
		if errors.Is(err, store.ErrTokenInvalid) || (err == nil && sess.ClientID != c.ID) {
			return ipxeScript(ipxe.Failure(arch, "the menu expired or was already used: unlock again.", true)), nil
		}
		if err != nil {
			return nil, err
		}
		p, err := s.store.Profile(ctx, *b.ProfileId)
		if errors.Is(err, store.ErrNotFound) {
			return ipxeScript(ipxe.Failure(arch, "that profile no longer exists.", true)), nil
		}
		if err != nil {
			return nil, err
		}
		if p.Kind != store.ProfileLinux {
			return ipxeScript(ipxe.Failure(arch, "profile "+p.Name+" isn't a Linux profile.", true)), nil
		}
		nd, prob, err := s.prepareDeployment(ctx, c, p, store.OriginBootPrompt)
		if err != nil {
			return nil, err
		}
		if prob != nil {
			return ipxeScript(ipxe.Failure(arch, "profile "+p.Name+" can't be deployed: "+ptrOr(prob.Detail, prob.Title), true)), nil
		}
		s.supersede(ctx, c.ID)
		if d, err = s.store.CreateDeployment(ctx, nd); errors.Is(err, store.ErrNotFound) {
			return ipxeScript(ipxe.Failure(arch, "profile "+p.Name+" can't be deployed: "+strings.TrimPrefix(err.Error(), "store: "), true)), nil
		} else if err != nil {
			return nil, err
		}
		s.deploymentEvent(d)
		log.Info("deployment started from the boot prompt", "deployment_id", d.ID, "client_id", c.ID, "profile", p.Name)
	}

	script, why, err := s.linuxBootScript(ctx, d, arch)
	if err != nil {
		return nil, err
	}
	if why != "" {
		if fd, err := s.store.FinishDeployment(context.WithoutCancel(ctx), d.ID, store.StatusFailed, "boot: "+why); err == nil {
			s.deploymentEvent(fd)
		}
		log.Warn("Linux deployment can't boot", "deployment_id", d.ID, "reason", why)
		return ipxeScript(ipxe.Failure(arch, why, false)), nil
	}
	return ipxeScript(script), nil
}

// supersede ends a client's active deployment before a boot prompt one
// (ADR 0013).
func (s *Server) supersede(ctx context.Context, clientID string) {
	d, err := s.store.ActiveDeployment(ctx, clientID)
	if err != nil {
		return
	}
	status, msg := store.StatusFailed, "superseded: a deployment was started from the boot prompt"
	if d.Status == store.StatusPending {
		status = store.StatusCancelled
	}
	if d, err := s.store.FinishDeployment(ctx, d.ID, status, msg); err == nil {
		s.deploymentEvent(d)
	}
}

// ipxeClient records the client by its boot NIC.
func (s *Server) ipxeClient(ctx context.Context, mac string, ip netip.Addr) (store.Client, error) {
	sg := store.Sighting{MAC: mac}
	if ip.IsValid() {
		ipStr := ip.String()
		sg.IP = &ipStr
	}
	c, created, err := s.store.RecordSighting(ctx, sg)
	if err != nil {
		return c, err
	}
	if created {
		s.clientEvent(c.ID, c.MAC, "created")
	}
	return c, nil
}

// linuxSnapshot is what a Linux deployment's boot commands need from its
// profile snapshot.
type linuxSnapshot struct {
	Kind           string  `json:"kind"`
	Name           string  `json:"name"`
	InstallImageID string  `json:"installImageId"`
	KernelArgs     *string `json:"kernelArgs"`
}

func snapshotIsLinux(raw json.RawMessage) bool {
	var s linuxSnapshot
	return json.Unmarshal(raw, &s) == nil && s.Kind == store.ProfileLinux
}

// linuxProfileBootable: the profile's image is deployable on arch.
func (s *Server) linuxProfileBootable(ctx context.Context, p store.Profile, arch string) bool {
	im, err := s.store.Image(ctx, p.InstallImageID)
	if err != nil {
		return false
	}
	d, reason := linuxImage(im)
	return reason == "" && d.Boot.Shim != nil && string(d.Architecture) == arch
}

// linuxBootScript builds a Linux deployment's boot commands from its
// snapshot. why says why it can't boot ("" when it can).
func (s *Server) linuxBootScript(ctx context.Context, d store.Deployment, arch string) (string, string, error) {
	var snap linuxSnapshot
	if err := json.Unmarshal(d.ProfileSnapshot, &snap); err != nil || snap.Kind != store.ProfileLinux {
		return "", "the deployment isn't a Linux one", nil
	}
	im, err := s.store.Image(ctx, snap.InstallImageID)
	if errors.Is(err, store.ErrNotFound) {
		return "", "its image no longer exists", nil
	}
	if err != nil {
		return "", "", err
	}
	det, reason := linuxImage(im)
	switch {
	case reason != "":
		return "", reason, nil
	case det.Boot.Shim == nil:
		return "", fmt.Sprintf("image %q has no shim", im.Name), nil
	case string(det.Architecture) != arch:
		return "", fmt.Sprintf("image %q is %s; this machine is %s", im.Name, det.Architecture, arch), nil
	}
	base := linuxPrefix + url.PathEscape(im.ID) + "/"
	file := func(p string) string { return base + strings.TrimPrefix(path.Clean("/"+p), "/") }
	cmdline := ""
	if det.Family == api.LinuxFamilyUbuntu {
		iso, why := s.isoURL(ctx, base+boot.ISOName)
		if why != "" {
			return "", why, nil
		}
		cmdline = "ip=dhcp url=" + iso + " cloud-config-url=/dev/null"
		if lp, err := autoinstall.ParseProfile(d.ProfileSnapshot); err == nil && lp.Automatic() {
			// The seed is found by a deployment token in its URL: the
			// installer's kernel has another address than iPXE (ADR 0035).
			tok, hash := newToken()
			if err := s.store.CreateAgentToken(ctx, store.AgentToken{Hash: hash, Kind: store.TokenDeployment, ClientID: d.ClientID,
				DeploymentID: &d.ID, ExpiresAt: time.Now().Add(deploymentTokenTTL).UTC()}); err != nil {
				return "", "", err
			}
			seed, why := s.isoURL(ctx, SeedPrefix+tok+"/")
			if why != "" {
				return "", why, nil
			}
			cmdline += " autoinstall ds=nocloud;s=" + seed
		}
	}
	if snap.KernelArgs != nil && strings.TrimSpace(*snap.KernelArgs) != "" {
		cmdline = strings.TrimSpace(cmdline + " " + ipxe.Text(*snap.KernelArgs))
	}
	return ipxe.LinuxBoot(ipxe.Boot{
		Title:     fmt.Sprintf("%s (%s)", snap.Name, im.Name),
		Shim:      file(det.Boot.Shim.SourcePath),
		Kernel:    file(det.Boot.Kernel.SourcePath),
		Initrd:    file(det.Boot.Initrd.SourcePath),
		Cmdline:   cmdline,
		ImageBase: base,
	}), "", nil
}

// isoURL is p on the plain HTTP listener of the address the request came
// in on: casper downloads the ISO with a wget that has no TLS (ADR 0034).
func (s *Server) isoURL(ctx context.Context, p string) (string, string) {
	local := localAddr(ctx)
	var eps []netip.AddrPort
	switch {
	case s.agent.httpEndpoints != nil: // tests
		eps = s.agent.httpEndpoints()
	case s.settings != nil:
		eps = s.settings.Endpoints(false)
	}
	for _, e := range eps {
		if e.Addr() == local {
			host := e.Addr().String()
			if e.Port() != 80 {
				host = netip.AddrPortFrom(e.Addr(), e.Port()).String()
			}
			return "http://" + host + p, ""
		}
	}
	return "", fmt.Sprintf("no plain HTTP content listener on %s, which the Ubuntu installer downloads its ISO from", local)
}

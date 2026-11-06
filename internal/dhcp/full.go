package dhcp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// pingAttempts caps how many conflicting addresses one DISCOVER skips.
const pingAttempts = 3

// errLeaseWrite: the binding couldn't be stored, so it must not be ACKed.
var errLeaseWrite = errors.New("lease not stored")

// full runs the full-mode state machine.
func (s *Server) full(ctx context.Context, req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, c client) (*dhcpv4.DHCPv4, string) {
	mac := req.ClientHWAddr.String()

	switch mt {
	case dhcpv4.MessageTypeDiscover:
		ip, why := s.offerAddress(ctx, mac, req.RequestedIPAddress())
		if ip == nil {
			return nil, why
		}
		ans, _, ok, reason := s.boot(ctx, req, c, nil)
		if !ok && reason != "" {
			s.log.Info("dhcp address only", "mac", mac, "reason", reason)
		}
		return s.reply(req, dhcpv4.MessageTypeOffer, ip, ans, ok), ""

	case dhcpv4.MessageTypeRequest:
		if sid := req.ServerIdentifier(); sid != nil && !sid.Equal(s.cfg.ServerIP) {
			// SELECTING state, client chose another server.
			s.mu.Lock()
			s.pool.release(mac)
			s.mu.Unlock()
			return nil, "client selected server " + sid.String()
		}
		ip := req.RequestedIPAddress()
		if ip == nil || ip.IsUnspecified() {
			ip = req.ClientIPAddr // RENEWING / REBINDING
		}
		if ip == nil || ip.IsUnspecified() {
			return nil, "request without an address"
		}
		switch err := s.bindAddress(ctx, req, mac, ip, c); {
		case errors.Is(err, errLeaseWrite):
			return nil, err.Error() // logged in bindAddress; the client retries
		case err != nil:
			// Includes INIT-REBOOT with an address from another network.
			return s.nak(req, "address not available"), ""
		}
		ans, arch, ok, _ := s.boot(ctx, req, c, ip)
		if ok {
			s.session(ip, mac, arch, ans)
		}
		return s.reply(req, dhcpv4.MessageTypeAck, ip, ans, ok), ""

	case dhcpv4.MessageTypeRelease:
		s.mu.Lock()
		s.pool.release(mac)
		s.mu.Unlock()
		if st := s.cfg.Store; st != nil {
			if err := st.ReleaseLease(ctx, s.cfg.Interface, mac); err != nil {
				s.log.Error("dhcp release not stored", "mac", mac, "err", err)
			}
		}
		return nil, "released"

	case dhcpv4.MessageTypeDecline:
		ip := req.RequestedIPAddress()
		s.decline(ctx, ip, mac)
		return nil, "declined " + ipString(ip)

	case dhcpv4.MessageTypeInform:
		return s.reply(req, dhcpv4.MessageTypeAck, nil, boot.Answer{}, false), ""
	}
	return nil, "unhandled message type"
}

// reservations returns IP -> MAC for every client reservation. A store
// error is logged and treated as "none": addressing carries on, at the risk
// of offering a reserved address, rather than stopping every client.
func (s *Server) reservations(ctx context.Context) map[uint32]string {
	out := map[uint32]string{}
	if s.cfg.Store == nil {
		return out
	}
	res, err := s.cfg.Store.ReservedIPs(ctx)
	if err != nil {
		s.log.Error("dhcp could not read reservations", "err", err)
		return out
	}
	for ip, mac := range res {
		if ip.Is4() {
			b := ip.As4()
			out[ip4ToUint(b[:])] = mac
		}
	}
	return out
}

// reservedFor returns mac's reserved address on this subnet, if any.
func (s *Server) reservedFor(res map[uint32]string, mac string) (uint32, bool) {
	for ip, m := range res {
		if m == mac && s.subnet.Contains(uintToIP4(ip)) {
			return ip, true
		}
	}
	return 0, false
}

// offerAddress picks an address for a DISCOVER: mac's reservation, else a
// pool address, ping-checking addresses with no lease history.
func (s *Server) offerAddress(ctx context.Context, mac string, requested net.IP) (net.IP, string) {
	res := s.reservations(ctx)
	now := s.now()

	if r, ok := s.reservedFor(res, mac); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		if holder, held := s.pool.holder(r, now); held && holder != mac {
			return nil, "reserved address " + uintToIP4(r).String() + " is leased to " + holder
		}
		s.pool.set(r, mac, now.Add(offerHold), false)
		return uintToIP4(r), ""
	}

	taken := func(u uint32) bool { m, ok := res[u]; return ok && m != mac }
	for range pingAttempts {
		s.mu.Lock()
		ip, fresh, ok := s.pool.offer(mac, requested, now, taken)
		s.mu.Unlock()
		if !ok {
			return nil, "pool exhausted"
		}
		if !fresh || !s.cfg.PingCheck || !s.inUse(ctx, ip) {
			return ip, ""
		}
		s.log.Warn("dhcp address answers ping; not offering it", "ip", ip.String())
		s.decline(ctx, ip, "")
		requested = nil
	}
	return nil, "every address tried answers ping"
}

// bindAddress commits ip to mac and stores the lease. Errors: errLeaseWrite
// if the store refused it, or a plain error if ip isn't available to mac.
func (s *Server) bindAddress(ctx context.Context, req *dhcpv4.DHCPv4, mac string, ip net.IP, c client) error {
	res := s.reservations(ctx)
	u := ip4ToUint(ip)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if m, reserved := res[u]; reserved && m != mac {
		return errors.New("reserved for another client")
	}
	if r, ok := s.reservedFor(res, mac); ok && r != u {
		return errors.New("client has a reservation for another address")
	}
	if _, reserved := res[u]; reserved {
		// A reservation may be outside the pool; it only needs to be ours.
		if holder, held := s.pool.holder(u, now); held && holder != mac {
			return errors.New("held by another client")
		}
	} else if !s.pool.free(u, mac, now) {
		return errors.New("not free")
	}

	expires := now.Add(s.cfg.LeaseTime)
	if st := s.cfg.Store; st != nil {
		addr, _ := netip.AddrFromSlice(ip.To4())
		l := store.Lease{Interface: s.cfg.Interface, IP: addr, MAC: mac, ExpiresAt: expires}
		if h := req.HostName(); h != "" {
			l.Hostname = &h
		}
		if arch, ok := c.bootArch(); ok {
			l.Arch = ptr(string(arch))
		}
		if err := st.PutLease(ctx, l); err != nil {
			s.log.Error("dhcp lease not stored; not acknowledging", "mac", mac, "ip", ip.String(), "err", err)
			return errLeaseWrite
		}
	}
	s.pool.set(u, mac, expires, true)
	return nil
}

// decline keeps ip out of use for declineHold (DHCPDECLINE, or a ping
// answer) and stores that.
func (s *Server) decline(ctx context.Context, ip net.IP, mac string) {
	if ip.To4() == nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	s.pool.decline(ip, now)
	s.mu.Unlock()
	if st := s.cfg.Store; st != nil {
		if mac == "" {
			mac = "00:00:00:00:00:00" // conflict found by ping: owner unknown
		}
		addr, _ := netip.AddrFromSlice(ip.To4())
		if err := st.DeclineLease(ctx, s.cfg.Interface, addr, mac, now.Add(declineHold)); err != nil {
			s.log.Error("dhcp decline not stored", "ip", ip.String(), "err", err)
		}
	}
}

// inUse pings ip. Without ICMP (no privilege) it warns once and reports
// false, so addressing carries on without the check.
func (s *Server) inUse(ctx context.Context, ip net.IP) bool {
	p := s.cfg.Pinger
	if p == nil {
		p = icmpPinger{}
	}
	alive, err := p.Ping(ctx, ip, 500*time.Millisecond)
	if err != nil {
		s.pingWarn.Do(func() {
			s.log.Warn("dhcp ping check unavailable; offering addresses without it", "err", err)
		})
		return false
	}
	return alive
}

// reply builds a full-mode OFFER or ACK. yiaddr is nil for INFORM; boot
// options are added when ok.
func (s *Server) reply(req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, yiaddr net.IP, ans boot.Answer, ok bool) *dhcpv4.DHCPv4 {
	mods := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithServerIP(s.cfg.ServerIP),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.cfg.ServerIP)),
		dhcpv4.WithNetmask(s.cfg.Netmask),
	}
	if yiaddr != nil {
		mods = append(mods,
			dhcpv4.WithYourIP(yiaddr),
			dhcpv4.WithLeaseTime(uint32(s.cfg.LeaseTime/time.Second)))
	}
	if s.cfg.Router != nil {
		mods = append(mods, dhcpv4.WithRouter(s.cfg.Router))
	}
	if len(s.cfg.DNS) > 0 {
		mods = append(mods, dhcpv4.WithDNS(s.cfg.DNS...))
	}
	if s.cfg.DomainName != "" {
		mods = append(mods, dhcpv4.WithOption(dhcpv4.OptDomainName(s.cfg.DomainName)))
	}
	file := ""
	if ok {
		switch {
		case ans.File != "":
			file = ans.File
			mods = append(mods,
				dhcpv4.WithOption(dhcpv4.OptTFTPServerName(s.cfg.ServerIP.String())),
				dhcpv4.WithOption(dhcpv4.OptBootFileName(file)))
			if s.cfg.PXEClientClass {
				mods = append(mods, dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient")))
			}
		case ans.URL != "":
			file = ans.URL
			mods = append(mods,
				dhcpv4.WithOption(dhcpv4.OptClassIdentifier("HTTPClient")),
				dhcpv4.WithOption(dhcpv4.OptBootFileName(file)))
		}
	}
	resp, err := dhcpv4.NewReplyFromRequest(req, mods...)
	if err != nil {
		// Only fails if the random transaction ID can't be generated,
		// which NewReplyFromRequest doesn't do. Treat as a bug.
		s.log.Error("dhcp build reply", "err", err)
		return nil
	}
	resp.BootFileName = file
	return resp
}

func (s *Server) nak(req *dhcpv4.DHCPv4, msg string) *dhcpv4.DHCPv4 {
	resp, err := dhcpv4.NewReplyFromRequest(req,
		dhcpv4.WithMessageType(dhcpv4.MessageTypeNak),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.cfg.ServerIP)),
		dhcpv4.WithOption(dhcpv4.OptMessage(msg)),
	)
	if err != nil {
		s.log.Error("dhcp build nak", "err", err)
		return nil
	}
	return resp
}

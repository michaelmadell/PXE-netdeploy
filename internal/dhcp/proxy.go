package dhcp

import (
	"context"
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
)

// pxeVendorOptions is option 43 for proxy offers: PXE_DISCOVERY_CONTROL (tag
// 6) = 0x08, "use the boot file in this offer; skip boot server discovery",
// then the end tag.
var pxeVendorOptions = []byte{6, 1, 0x08, 255}

// proxy runs proxy mode (PXE 2.1 proxyDHCP). Untested against real firmware:
// see docs/dev/running-the-lab.md for the lab run.
func (s *Server) proxy(ctx context.Context, req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, c client, peer *net.UDPAddr, localPort int) (*dhcpv4.DHCPv4, *net.UDPAddr, string) {
	mac := req.ClientHWAddr.String()

	switch {
	case localPort == ProxyPort && mt == dhcpv4.MessageTypeRequest:
		// Boot server REQUEST: the client already has an address.
		ans, arch, ok, why := s.boot(ctx, req, c, req.ClientIPAddr)
		if !ok {
			return nil, nil, why
		}
		s.session(req.ClientIPAddr, mac, arch, ans)
		resp := s.proxyReply(req, dhcpv4.MessageTypeAck, ans, c)
		if resp == nil {
			return nil, nil, "could not build reply"
		}
		dst := peer
		if dst == nil || dst.IP.IsUnspecified() {
			dst = &net.UDPAddr{IP: req.ClientIPAddr, Port: ProxyPort}
		}
		return resp, dst, ""

	case localPort != ProxyPort && mt == dhcpv4.MessageTypeDiscover:
		ans, _, ok, why := s.boot(ctx, req, c, nil)
		if !ok {
			return nil, nil, why
		}
		resp := s.proxyReply(req, dhcpv4.MessageTypeOffer, ans, c)
		if resp == nil {
			return nil, nil, "could not build reply"
		}
		return resp, replyAddr(req, resp), ""

	case localPort != ProxyPort && mt == dhcpv4.MessageTypeRequest:
		// The client's REQUEST to the real DHCP server: never answered, but
		// it tells us which address the client is taking, for TFTP.
		ip := req.RequestedIPAddress()
		if ip == nil || ip.IsUnspecified() {
			ip = req.ClientIPAddr
		}
		if ip == nil || ip.IsUnspecified() {
			return nil, nil, "request without an address"
		}
		ans, arch, ok, why := s.boot(ctx, req, c, ip)
		if !ok {
			return nil, nil, why
		}
		s.session(ip, mac, arch, ans)
		return nil, nil, "request for the real DHCP server; noted " + ip.String() + " for boot"
	}
	return nil, nil, "not a proxyDHCP message"
}

// proxyReply builds a proxy OFFER or a boot server ACK: no address, the boot
// answer, option 60 naming the boot protocol, and option 97 echoed (PXE spec
// 2.1; some firmware ignores offers without it).
func (s *Server) proxyReply(req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, ans boot.Answer, c client) *dhcpv4.DHCPv4 {
	mods := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithServerIP(s.cfg.ServerIP),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.cfg.ServerIP)),
	}
	if c.uuidRaw != nil {
		mods = append(mods, dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, c.uuidRaw)))
	}
	var file string
	if ans.URL != "" {
		file = ans.URL
		mods = append(mods,
			dhcpv4.WithOption(dhcpv4.OptClassIdentifier("HTTPClient")),
			dhcpv4.WithOption(dhcpv4.OptBootFileName(file)))
	} else {
		file = ans.File
		mods = append(mods,
			dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient")),
			dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionVendorSpecificInformation, pxeVendorOptions)),
			dhcpv4.WithOption(dhcpv4.OptTFTPServerName(s.cfg.ServerIP.String())),
			dhcpv4.WithOption(dhcpv4.OptBootFileName(file)))
	}
	resp, err := dhcpv4.NewReplyFromRequest(req, mods...)
	if err != nil {
		s.log.Error("dhcp build proxy reply", "err", err)
		return nil
	}
	resp.YourIPAddr = net.IPv4zero
	resp.BootFileName = file
	return resp
}

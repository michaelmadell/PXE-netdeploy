package settings

import "net/netip"

// HTTPEndpoints lists where a booted machine can reach the content
// listener and agent API: for each interface serving HTTP, its address on
// this host and its advertisedAddress, at its HTTP port. Interfaces that
// don't resolve on this host contribute only an advertisedAddress.
func (s *Service) HTTPEndpoints() []netip.AddrPort {
	cfg, _ := s.Current()
	var out []netip.AddrPort
	for _, in := range cfg.Interfaces {
		if !in.Services.HTTP {
			continue
		}
		port := uint16(in.Services.HTTPPort)
		if in.AdvertisedAddress.IsValid() {
			out = append(out, netip.AddrPortFrom(in.AdvertisedAddress, port))
		}
		if hi, err := s.opts.Host.Interface(in.Name); err == nil {
			if p, ok := hi.Primary(); ok {
				out = append(out, netip.AddrPortFrom(p.Addr(), port))
			}
		}
	}
	return out
}

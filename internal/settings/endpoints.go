package settings

import "net/netip"

// HTTPEndpoints lists where a booted machine can reach the content
// listener and agent API over HTTP (Endpoints(false)).
func (s *Service) HTTPEndpoints() []netip.AddrPort { return s.Endpoints(false) }

// Endpoints lists where a booted machine can reach the content listener
// and agent API over HTTP, or over HTTPS when https is set: for each
// interface serving it, its address on this host and its
// advertisedAddress, at its port. Interfaces that don't resolve on this
// host contribute only an advertisedAddress.
func (s *Service) Endpoints(https bool) []netip.AddrPort {
	cfg, _ := s.Current()
	var out []netip.AddrPort
	for _, in := range cfg.Interfaces {
		on, port := in.Services.HTTP, uint16(in.Services.HTTPPort)
		if https {
			on, port = in.Services.HTTPS, uint16(in.Services.HTTPSPort)
		}
		if !on {
			continue
		}
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

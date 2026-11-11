package server

import (
	"context"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

// ListHostInterfaces implements GET /system/interfaces: the machine's
// network interfaces, read from the OS on every call (they change when
// cables, VPNs and virtual switches do).
func (s *Server) ListHostInterfaces(_ context.Context, _ api.ListHostInterfacesRequestObject) (api.ListHostInterfacesResponseObject, error) {
	list, err := s.host.Interfaces()
	if err != nil {
		return nil, err
	}
	out := api.ListHostInterfaces200JSONResponse{Items: make([]api.HostInterface, len(list))}
	for i, h := range list {
		hi := api.HostInterface{Name: h.Name, Up: h.Up, Loopback: h.Loopback,
			Addresses: make([]api.HostInterfaceAddress, len(h.Prefixes))}
		if h.MAC != "" {
			mac := h.MAC
			hi.Mac = &mac
		}
		for j, p := range h.Prefixes {
			hi.Addresses[j] = api.HostInterfaceAddress{Address: p.Addr().String(), PrefixLength: p.Bits()}
		}
		out.Items[i] = hi
	}
	return out, nil
}

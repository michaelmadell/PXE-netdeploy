package settings

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

// FromAPI turns a PUT /settings body into a Config, filling defaults and
// resolving write-only fields against cur (the running config):
//
//   - bootPrompt.password: nil keeps cur's hash; a value is hashed.
//   - admin / contentHttps certificatePem + privateKeyPem: both set replaces
//     the pair; certificatePem alone keeps cur's pair if it is the same
//     certificate (a GET echoed back); both null means "not custom".
//
// Read-only fields (etag, hasPassword, externalDhcpGuidance) are ignored.
// Schema rules (ranges, enums, patterns) are the request validator's job;
// FromAPI only reports what it can't parse or pair up.
func FromAPI(in api.Settings, cur Config) (Config, []FieldError) {
	var errs fieldErrors
	out := Defaults()

	if a, err := netip.ParseAddr(in.Admin.BindAddress); err != nil || !a.Is4() {
		errs.add("admin.bindAddress", "must be an IPv4 address")
	} else {
		out.Admin.BindAddress = a
	}
	out.Admin.Port = in.Admin.Port
	out.Admin.Certificate = certPair("admin", in.Admin.CertificatePem, in.Admin.PrivateKeyPem, cur.Admin.Certificate, &errs)
	if in.ContentHttps != nil {
		out.ContentHTTPS = certPair("contentHttps", in.ContentHttps.CertificatePem, in.ContentHttps.PrivateKeyPem, cur.ContentHTTPS, &errs)
	}

	out.Interfaces = make([]Interface, 0, len(in.Interfaces))
	for i, ai := range in.Interfaces {
		out.Interfaces = append(out.Interfaces, interfaceFromAPI(fmt.Sprintf("interfaces[%d]", i), ai, &errs))
	}

	if t := in.Tftp; t != nil {
		setInt(&out.TFTP.MaxBlockSize, t.MaxBlockSize)
		setInt(&out.TFTP.MaxWindowSize, t.MaxWindowSize)
		setInt(&out.TFTP.TimeoutSeconds, t.TimeoutSeconds)
	}

	out.BootPrompt.PasswordHash = cur.BootPrompt.PasswordHash
	if b := in.BootPrompt; b != nil {
		if b.Password != nil {
			h, err := HashPassword(*b.Password)
			if err != nil {
				errs.add("bootPrompt.password", "%v", err)
			}
			out.BootPrompt.PasswordHash = h
		}
		setBool(&out.BootPrompt.RequirePassword, b.RequirePassword)
		setInt(&out.BootPrompt.TimeoutSeconds, b.TimeoutSeconds)
		setInt(&out.BootPrompt.ArmedCountdownSeconds, b.ArmedCountdownSeconds)
		if b.DefaultProfileId != nil {
			out.BootPrompt.DefaultProfileID = *b.DefaultProfileId
		}
	}

	if a := in.Architectures; a != nil {
		setBool(&out.Architectures.BIOS, a.Bios)
		setBool(&out.Architectures.UEFIX64, a.UefiX64)
		setBool(&out.Architectures.UEFIArm64, a.UefiArm64)
		setBool(&out.Architectures.HTTPBoot, a.HttpBoot)
	}
	if st := in.Storage; st != nil {
		if st.ReserveBytes != nil {
			out.Storage.ReserveBytes = *st.ReserveBytes
		}
		setInt(&out.Storage.UploadExpiryHours, st.UploadExpiryHours)
	}
	return out, errs.list()
}

func interfaceFromAPI(path string, ai api.InterfaceSettings, errs *fieldErrors) Interface {
	out := Interface{Name: ai.Name, Services: defaultServices(), DHCP: DHCP{Mode: defaultDHCPMode, ListenOn67: true}}
	if ai.AdvertisedAddress != nil {
		out.AdvertisedAddress = parseIPv4(path+".advertisedAddress", *ai.AdvertisedAddress, errs)
	}
	if ai.MacPrefixes != nil {
		for _, p := range *ai.MacPrefixes {
			out.MACPrefixes = append(out.MACPrefixes, strings.ToLower(p))
		}
	}
	s := ai.Services
	setBool(&out.Services.TFTP, s.Tftp)
	setBool(&out.Services.HTTP, s.Http)
	setInt(&out.Services.HTTPPort, s.HttpPort)
	setBool(&out.Services.HTTPS, s.Https)
	setInt(&out.Services.HTTPSPort, s.HttpsPort)

	if ai.Dhcp.Mode != "" {
		out.DHCP.Mode = string(ai.Dhcp.Mode)
	}
	setBool(&out.DHCP.ListenOn67, ai.Dhcp.ListenOn67)
	if p := ai.Dhcp.Pool; p != nil {
		pp := path + ".dhcp.pool"
		pool := &Pool{
			Start:        parseIPv4(pp+".start", p.Start, errs),
			End:          parseIPv4(pp+".end", p.End, errs),
			SubnetMask:   parseIPv4(pp+".subnetMask", p.SubnetMask, errs),
			LeaseSeconds: defaultLeaseSeconds,
			PingCheck:    true,
		}
		if p.Router != nil {
			pool.Router = parseIPv4(pp+".router", *p.Router, errs)
		}
		if p.DnsServers != nil {
			for j, d := range *p.DnsServers {
				pool.DNSServers = append(pool.DNSServers, parseIPv4(fmt.Sprintf("%s.dnsServers[%d]", pp, j), d, errs))
			}
		}
		if p.DomainName != nil {
			pool.DomainName = *p.DomainName
		}
		setInt(&pool.LeaseSeconds, p.LeaseSeconds)
		setBool(&pool.PingCheck, p.PingCheck)
		out.DHCP.Pool = pool
	}
	return out
}

func certPair(path string, certPEM, keyPEM *string, cur CertPair, errs *fieldErrors) CertPair {
	switch {
	case certPEM == nil && keyPEM == nil:
		return CertPair{}
	case certPEM == nil:
		errs.add(path+".certificatePem", "required with privateKeyPem")
		return cur
	case keyPEM == nil:
		if cur.Custom() && strings.TrimSpace(*certPEM) == strings.TrimSpace(cur.CertificatePEM) {
			return cur // unchanged, e.g. a GET sent back
		}
		errs.add(path+".privateKeyPem", "required with a new certificatePem")
		return cur
	}
	if _, err := tls.X509KeyPair([]byte(*certPEM), []byte(*keyPEM)); err != nil {
		errs.add(path+".certificatePem", "certificate and private key don't form a valid pair: %v", err)
		return cur
	}
	return CertPair{CertificatePEM: *certPEM, PrivateKeyPEM: *keyPEM}
}

func parseIPv4(field, s string, errs *fieldErrors) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		errs.add(field, "must be an IPv4 address")
		return netip.Addr{}
	}
	return a
}

func setInt(dst *int, v *int) {
	if v != nil {
		*dst = *v
	}
}

func setBool(dst *bool, v *bool) {
	if v != nil {
		*dst = *v
	}
}

// Guidance is one externalDhcpGuidance entry.
type Guidance struct {
	Arch        api.ClientArch `json:"arch"`
	NextServer  string         `json:"nextServer"`
	BootFile    string         `json:"bootFile"`
	VendorClass string         `json:"vendorClass"`
}

// ToAPI renders cfg as the API's Settings: write-only fields omitted,
// hasPassword and the current custom certificates filled in, and guidance
// (keyed by interface name) attached to interfaces in `off` mode.
func ToAPI(cfg Config, etag string, guidance map[string][]Guidance) (api.Settings, error) {
	// The generated type nests anonymous structs, so build the JSON shape and
	// decode it rather than constructing them field by field.
	type pool struct {
		Start        string   `json:"start"`
		End          string   `json:"end"`
		SubnetMask   string   `json:"subnetMask"`
		Router       *string  `json:"router"`
		DNSServers   []string `json:"dnsServers"`
		DomainName   *string  `json:"domainName"`
		LeaseSeconds int      `json:"leaseSeconds"`
		PingCheck    bool     `json:"pingCheck"`
	}
	type iface struct {
		Name              string     `json:"name"`
		AdvertisedAddress *string    `json:"advertisedAddress"`
		MACPrefixes       []string   `json:"macPrefixes"`
		Services          Services   `json:"services"`
		DHCP              any        `json:"dhcp"`
		Guidance          []Guidance `json:"externalDhcpGuidance,omitempty"`
	}
	ifaces := make([]iface, 0, len(cfg.Interfaces))
	for _, in := range cfg.Interfaces {
		o := iface{Name: in.Name, MACPrefixes: in.MACPrefixes, Services: in.Services, Guidance: guidance[in.Name]}
		if o.MACPrefixes == nil {
			o.MACPrefixes = []string{}
		}
		if in.AdvertisedAddress.IsValid() {
			o.AdvertisedAddress = ptr(in.AdvertisedAddress.String())
		}
		d := map[string]any{"mode": in.DHCP.Mode, "listenOn67": in.DHCP.ListenOn67, "pool": nil}
		if p := in.DHCP.Pool; p != nil {
			op := pool{
				Start: p.Start.String(), End: p.End.String(), SubnetMask: p.SubnetMask.String(),
				DNSServers: []string{}, LeaseSeconds: p.LeaseSeconds, PingCheck: p.PingCheck,
			}
			if p.Router.IsValid() {
				op.Router = ptr(p.Router.String())
			}
			for _, a := range p.DNSServers {
				op.DNSServers = append(op.DNSServers, a.String())
			}
			if p.DomainName != "" {
				op.DomainName = ptr(p.DomainName)
			}
			d["pool"] = op
		}
		o.DHCP = d
		ifaces = append(ifaces, o)
	}
	nullable := func(c CertPair) *string {
		if c.Custom() {
			return ptr(c.CertificatePEM)
		}
		return nil
	}
	var defaultProfile *string
	if cfg.BootPrompt.DefaultProfileID != "" {
		defaultProfile = ptr(cfg.BootPrompt.DefaultProfileID)
	}
	doc := map[string]any{
		"etag": etag,
		"admin": map[string]any{
			"bindAddress":    cfg.Admin.BindAddress.String(),
			"port":           cfg.Admin.Port,
			"certificatePem": nullable(cfg.Admin.Certificate),
		},
		"interfaces":   ifaces,
		"tftp":         cfg.TFTP,
		"contentHttps": map[string]any{"certificatePem": nullable(cfg.ContentHTTPS)},
		"bootPrompt": map[string]any{
			"hasPassword":           cfg.BootPrompt.PasswordHash != "",
			"requirePassword":       cfg.BootPrompt.RequirePassword,
			"timeoutSeconds":        cfg.BootPrompt.TimeoutSeconds,
			"armedCountdownSeconds": cfg.BootPrompt.ArmedCountdownSeconds,
			"defaultProfileId":      defaultProfile,
		},
		"architectures": cfg.Architectures,
		"storage":       cfg.Storage,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return api.Settings{}, fmt.Errorf("settings: render: %w", err)
	}
	var out api.Settings
	if err := json.Unmarshal(b, &out); err != nil {
		return api.Settings{}, fmt.Errorf("settings: render: %w", err)
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

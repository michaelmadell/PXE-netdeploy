// Package settings owns netdeployd's configuration: the stored form, its
// defaults, conversion to and from the API's Settings, validation against the
// host, and applying it to the network listeners (see Service).
package settings

import (
	"net/netip"
)

// Config is the stored settings document (ADR 0014) with every default
// resolved. It is not the API type: secrets are kept in the form the server
// needs (a password hash, custom certificate PEMs) and read-only API fields
// are computed, not stored. Field names are part of the stored format; rename
// them only with a migration.
type Config struct {
	Admin         Admin         `json:"admin"`
	Interfaces    []Interface   `json:"interfaces"`
	TFTP          TFTP          `json:"tftp"`
	ContentHTTPS  CertPair      `json:"contentHttps"`
	BootPrompt    BootPrompt    `json:"bootPrompt"`
	Architectures Architectures `json:"architectures"`
}

// Admin is the admin API + UI listener.
type Admin struct {
	BindAddress netip.Addr `json:"bindAddress"`
	Port        int        `json:"port"`
	// Certificate is the custom certificate; empty uses the generated one
	// in the data directory.
	Certificate CertPair `json:"certificate"`
}

// CertPair is a PEM certificate chain and its private key. Both empty means
// "not set" (generated or not configured).
type CertPair struct {
	CertificatePEM string `json:"certificatePem,omitempty"`
	PrivateKeyPEM  string `json:"privateKeyPem,omitempty"`
}

// Custom reports whether a pair is set.
func (c CertPair) Custom() bool { return c.CertificatePEM != "" }

// Interface is one deployment-network interface.
type Interface struct {
	Name string `json:"name"`
	// AdvertisedAddress is what clients use to reach this server; zero uses
	// the interface's primary IPv4 address.
	AdvertisedAddress netip.Addr `json:"advertisedAddress"`
	MACPrefixes       []string   `json:"macPrefixes"`
	Services          Services   `json:"services"`
	DHCP              DHCP       `json:"dhcp"`
}

// Services are the content services on an interface.
type Services struct {
	TFTP      bool `json:"tftp"`
	HTTP      bool `json:"http"`
	HTTPPort  int  `json:"httpPort"`
	HTTPS     bool `json:"https"`
	HTTPSPort int  `json:"httpsPort"`
}

// DHCP modes.
const (
	DHCPOff   = "off"
	DHCPProxy = "proxy"
	DHCPFull  = "full"
)

// DHCP is an interface's DHCP mode.
type DHCP struct {
	Mode       string `json:"mode"`
	ListenOn67 bool   `json:"listenOn67"`
	Pool       *Pool  `json:"pool"`
}

// Pool is a full-mode address pool.
type Pool struct {
	Start        netip.Addr   `json:"start"`
	End          netip.Addr   `json:"end"`
	SubnetMask   netip.Addr   `json:"subnetMask"`
	Router       netip.Addr   `json:"router"` // zero: none
	DNSServers   []netip.Addr `json:"dnsServers"`
	DomainName   string       `json:"domainName"`
	LeaseSeconds int          `json:"leaseSeconds"`
	PingCheck    bool         `json:"pingCheck"`
}

// TFTP holds the server-side TFTP caps.
type TFTP struct {
	MaxBlockSize   int `json:"maxBlockSize"`
	MaxWindowSize  int `json:"maxWindowSize"`
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// BootPrompt is the WinPE boot prompt (ADR 0013).
type BootPrompt struct {
	// PasswordHash is a PHC-format argon2id hash; empty means no password.
	PasswordHash          string `json:"passwordHash,omitempty"`
	RequirePassword       bool   `json:"requirePassword"`
	TimeoutSeconds        int    `json:"timeoutSeconds"`
	ArmedCountdownSeconds int    `json:"armedCountdownSeconds"`
	DefaultProfileID      string `json:"defaultProfileId,omitempty"`
}

// Architectures says which client architectures are answered.
type Architectures struct {
	BIOS      bool `json:"bios"`
	UEFIX64   bool `json:"uefiX64"`
	UEFIArm64 bool `json:"uefiArm64"`
	HTTPBoot  bool `json:"httpBoot"`
}

// Defaults are the spec's defaults: no interfaces, so a fresh install answers
// nobody, and the admin listener on localhost.
func Defaults() Config {
	return Config{
		Admin:      Admin{BindAddress: netip.MustParseAddr("127.0.0.1"), Port: 8443},
		Interfaces: []Interface{},
		TFTP:       TFTP{MaxBlockSize: 8192, MaxWindowSize: 8, TimeoutSeconds: 1},
		BootPrompt: BootPrompt{RequirePassword: true, TimeoutSeconds: 30, ArmedCountdownSeconds: 10},
		Architectures: Architectures{
			BIOS: false, UEFIX64: true, UEFIArm64: true, HTTPBoot: true,
		},
	}
}

func defaultServices() Services {
	return Services{TFTP: true, HTTP: true, HTTPPort: 80, HTTPS: false, HTTPSPort: 443}
}

const (
	defaultLeaseSeconds = 3600
	defaultDHCPMode     = DHCPProxy
)

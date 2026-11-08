package settings

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

func init() {
	// Keep the tests fast; the format and verification are the same.
	hashParams = argonParams{memoryKiB: 64, time: 1, threads: 1, saltLen: 16, keyLen: 32}
}

// fakeHost has one interface, lab0 = 10.77.0.1/24.
type fakeHost map[string]HostInterface

func (h fakeHost) Interface(name string) (HostInterface, error) {
	if hi, ok := h[name]; ok {
		return hi, nil
	}
	return HostInterface{}, fmt.Errorf("%w %q", ErrNoInterface, name)
}

var lab = fakeHost{
	"lab0":   {Name: "lab0", Prefixes: []netip.Prefix{netip.MustParsePrefix("10.77.0.1/24")}},
	"noaddr": {Name: "noaddr"},
}

// apiSettings decodes a JSON document into api.Settings.
func apiSettings(t *testing.T, doc string) api.Settings {
	t.Helper()
	var s api.Settings
	if err := json.Unmarshal([]byte(doc), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

const minimal = `{"admin":{"bindAddress":"127.0.0.1","port":8443},"interfaces":[]}`

func TestFromAPIFillsDefaults(t *testing.T) {
	cfg, errs := FromAPI(apiSettings(t, `{"admin":{"bindAddress":"127.0.0.1","port":8443},
		"interfaces":[{"name":"lab0","dhcp":{"mode":"full","pool":{"start":"10.77.0.100","end":"10.77.0.199","subnetMask":"255.255.255.0"}},"services":{}}]}`), Defaults())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	in := cfg.Interfaces[0]
	if !in.Services.TFTP || !in.Services.HTTP || in.Services.HTTPPort != 80 || in.Services.HTTPS {
		t.Errorf("services defaults: %+v", in.Services)
	}
	if !in.DHCP.ListenOn67 || in.DHCP.Pool.LeaseSeconds != 3600 || !in.DHCP.Pool.PingCheck {
		t.Errorf("dhcp defaults: %+v %+v", in.DHCP, in.DHCP.Pool)
	}
	if cfg.TFTP != (TFTP{MaxBlockSize: 8192, MaxWindowSize: 8, TimeoutSeconds: 1}) {
		t.Errorf("tftp defaults: %+v", cfg.TFTP)
	}
	if !cfg.BootPrompt.RequirePassword || cfg.BootPrompt.TimeoutSeconds != 30 || cfg.BootPrompt.ArmedCountdownSeconds != 10 {
		t.Errorf("boot prompt defaults: %+v", cfg.BootPrompt)
	}
	if cfg.Storage.ReserveBytes != 5<<30 || cfg.Storage.UploadExpiryHours != 24 {
		t.Errorf("storage defaults: %+v", cfg.Storage)
	}
	if errs := Validate(cfg, lab); len(errs) > 0 {
		t.Errorf("Validate: %v", errs)
	}
}

func TestRoundTripKeepsEverything(t *testing.T) {
	doc := `{"admin":{"bindAddress":"10.0.0.5","port":9443},
		"interfaces":[{"name":"lab0","advertisedAddress":"203.0.113.9","macPrefixes":["00:1B:21"],
			"dhcp":{"mode":"full","pool":{"start":"10.77.0.100","end":"10.77.0.199","subnetMask":"255.255.255.0",
				"router":"10.77.0.254","dnsServers":["10.77.0.53"],"domainName":"lab","leaseSeconds":600,"pingCheck":false}},
			"services":{"tftp":true,"http":false,"httpPort":8080,"https":true,"httpsPort":8443}}],
		"tftp":{"maxBlockSize":1456,"maxWindowSize":16,"timeoutSeconds":2},
		"bootPrompt":{"requirePassword":false,"timeoutSeconds":60,"armedCountdownSeconds":0,"defaultProfileId":"p1"},
		"architectures":{"bios":true,"uefiX64":true,"uefiArm64":false,"httpBoot":false},
		"storage":{"reserveBytes":1073741824,"uploadExpiryHours":72},
		"backup":{"schedule":{"frequency":"weekly","time":"03:15","daysOfWeek":["mon","thu"]},"retention":0,
			"destination":{"type":"smb","path":"//nas01/backups/nd","credentialSecretId":"s1"}}}`
	cfg, errs := FromAPI(apiSettings(t, doc), Defaults())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	out, err := ToAPI(cfg, `"3"`, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, errs := FromAPI(out, cfg)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	a, _ := json.Marshal(cfg)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Errorf("round trip changed the config:\n%s\n%s", a, b)
	}
	if cfg.Storage.ReserveBytes != 1<<30 || cfg.Storage.UploadExpiryHours != 72 {
		t.Errorf("storage: %+v", cfg.Storage)
	}
	if b := cfg.Backup; b.Retention != 0 || b.Schedule.Frequency != "weekly" || b.Destination == nil ||
		b.Destination.Path != `\\nas01\backups\nd` || b.Destination.CredentialSecretID != "s1" || b.Schedule.DayOfMonth != 1 {
		t.Errorf("backup: %+v %+v", b, b.Destination)
	}
	if got := cfg.Interfaces[0].MACPrefixes[0]; got != "00:1b:21" {
		t.Errorf("mac prefix not normalised: %q", got)
	}
	if *out.Etag != `"3"` {
		t.Errorf("etag %q", *out.Etag)
	}
}

func TestPasswordIsHashedAndKept(t *testing.T) {
	cfg, errs := FromAPI(apiSettings(t, `{"admin":{"bindAddress":"127.0.0.1","port":8443},"interfaces":[],
		"bootPrompt":{"password":"correct horse"}}`), Defaults())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	h := cfg.BootPrompt.PasswordHash
	if !strings.HasPrefix(h, "$argon2id$v=19$") || strings.Contains(h, "correct horse") {
		t.Fatalf("hash %q", h)
	}
	if ok, err := VerifyPassword(h, "correct horse"); err != nil || !ok {
		t.Fatalf("VerifyPassword(right) = %v, %v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Fatal("VerifyPassword accepted a wrong password")
	}

	// Omitted or null keeps it; GET never returns it.
	kept, _ := FromAPI(apiSettings(t, minimal), cfg)
	if kept.BootPrompt.PasswordHash != h {
		t.Error("omitting password dropped the hash")
	}
	out, _ := ToAPI(kept, "", nil)
	if out.BootPrompt.Password != nil || !*out.BootPrompt.HasPassword {
		t.Errorf("rendered password=%v hasPassword=%v", out.BootPrompt.Password, *out.BootPrompt.HasPassword)
	}
	if _, err := VerifyPassword("not-a-hash", "x"); err == nil {
		t.Error("VerifyPassword accepted a malformed hash")
	}
}

func selfSigned(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
}

func TestAdminCertificatePairing(t *testing.T) {
	c, k := selfSigned(t)
	_, otherKey := selfSigned(t)
	body := func(cert, key *string) api.Settings {
		s := apiSettings(t, minimal)
		s.Admin.CertificatePem, s.Admin.PrivateKeyPem = cert, key
		return s
	}

	cfg, errs := FromAPI(body(&c, &k), Defaults())
	if len(errs) > 0 || !cfg.Admin.Certificate.Custom() {
		t.Fatalf("set pair: %v %+v", errs, cfg.Admin.Certificate)
	}
	// GET returns the certificate (not the key); sending it back keeps the pair.
	out, _ := ToAPI(cfg, "", nil)
	if out.Admin.CertificatePem == nil || out.Admin.PrivateKeyPem != nil {
		t.Fatal("GET must return the certificate and never the key")
	}
	kept, errs := FromAPI(body(out.Admin.CertificatePem, nil), cfg)
	if len(errs) > 0 || kept.Admin.Certificate != cfg.Admin.Certificate {
		t.Fatalf("echoed certificate: %v", errs)
	}
	// Null goes back to the generated certificate.
	if gen, _ := FromAPI(body(nil, nil), cfg); gen.Admin.Certificate.Custom() {
		t.Error("null certificate kept the custom pair")
	}

	for name, tc := range map[string]struct{ cert, key *string }{
		"new cert without key": {ptr("-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n"), nil},
		"key without cert":     {nil, &k},
		"mismatched pair":      {&c, &otherKey},
	} {
		if _, errs := FromAPI(body(tc.cert, tc.key), cfg); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidate(t *testing.T) {
	pool := func(start, end, mask string) *Pool {
		return &Pool{Start: netip.MustParseAddr(start), End: netip.MustParseAddr(end), SubnetMask: netip.MustParseAddr(mask)}
	}
	iface := func(name, mode string, p *Pool) Interface {
		return Interface{Name: name, Services: defaultServices(), DHCP: DHCP{Mode: mode, Pool: p}}
	}
	cases := []struct {
		name  string
		ifs   []Interface
		admin netip.Addr
		want  string // substring of the first error's field; "" means valid
	}{
		{"off mode", []Interface{iface("lab0", DHCPOff, nil)}, netip.Addr{}, ""},
		{"full mode", []Interface{iface("lab0", DHCPFull, pool("10.77.0.100", "10.77.0.199", "255.255.255.0"))}, netip.Addr{}, ""},
		{"unknown interface", []Interface{iface("eth9", DHCPOff, nil)}, netip.Addr{}, "interfaces[0].name"},
		{"no ipv4", []Interface{iface("noaddr", DHCPOff, nil)}, netip.Addr{}, "interfaces[0].name"},
		{"duplicate", []Interface{iface("lab0", DHCPOff, nil), iface("LAB0", DHCPOff, nil)}, netip.Addr{}, "interfaces[1].name"},
		{"proxy mode", []Interface{iface("lab0", DHCPProxy, nil)}, netip.Addr{}, ""},
		{"full without pool", []Interface{iface("lab0", DHCPFull, nil)}, netip.Addr{}, "interfaces[0].dhcp.pool"},
		{"pool outside subnet", []Interface{iface("lab0", DHCPFull, pool("10.77.0.100", "10.77.1.5", "255.255.255.0"))}, netip.Addr{}, "interfaces[0].dhcp.pool.end"},
		{"mask mismatch", []Interface{iface("lab0", DHCPFull, pool("10.77.0.100", "10.77.0.199", "255.255.0.0"))}, netip.Addr{}, "interfaces[0].dhcp.pool.subnetMask"},
		{"pool has server", []Interface{iface("lab0", DHCPFull, pool("10.77.0.1", "10.77.0.10", "255.255.255.0"))}, netip.Addr{}, "interfaces[0].dhcp.pool"},
		{"end before start", []Interface{iface("lab0", DHCPFull, pool("10.77.0.200", "10.77.0.100", "255.255.255.0"))}, netip.Addr{}, "interfaces[0].dhcp.pool.end"},
		{"broadcast", []Interface{iface("lab0", DHCPFull, pool("10.77.0.100", "10.77.0.255", "255.255.255.0"))}, netip.Addr{}, "interfaces[0].dhcp.pool.end"},
		{"admin on content port", []Interface{iface("lab0", DHCPOff, nil)}, netip.MustParseAddr("0.0.0.0"), "interfaces[0].services.httpPort"},
		{"https not yet", []Interface{func() Interface { i := iface("lab0", DHCPOff, nil); i.Services.HTTPS = true; return i }()}, netip.Addr{}, "interfaces[0].services.https"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Interfaces = tc.ifs
			if tc.admin.IsValid() {
				cfg.Admin.BindAddress, cfg.Admin.Port = tc.admin, 80
			}
			errs := Validate(cfg, lab)
			if tc.want == "" {
				if len(errs) > 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			if len(errs) == 0 || !strings.Contains(errs[0].Field, tc.want) {
				t.Fatalf("errors %v, want first on %s", errs, tc.want)
			}
		})
	}
}

func TestETagMatching(t *testing.T) {
	for header, want := range map[string]bool{`"4"`: true, `"3"`: false, `*`: true, `"1", "4"`: true, `4`: false, ``: false} {
		if got := etagMatches(header, 4); got != want {
			t.Errorf("etagMatches(%q, 4) = %v", header, got)
		}
	}
}

func TestBackupSettingsValidation(t *testing.T) {
	base := `{"admin":{"bindAddress":"127.0.0.1","port":8443},"interfaces":[],"backup":%s}`
	for body, field := range map[string]string{
		`{"schedule":{"frequency":"weekly","daysOfWeek":[]}}`:                      "backup.schedule.daysOfWeek",
		`{"destination":{"type":"smb","path":"C:\\x","credentialSecretId":"s"}}`:   "backup.destination.path",
		`{"destination":{"type":"smb","path":"\\\\nas\\b"}}`:                       "backup.destination.credentialSecretId",
		`{"destination":{"type":"local","path":"D:\\b","credentialSecretId":"s"}}`: "backup.destination.credentialSecretId",
	} {
		_, errs := FromAPI(apiSettings(t, fmt.Sprintf(base, body)), Defaults())
		if len(errs) != 1 || errs[0].Field != field {
			t.Errorf("%s: %v", body, errs)
		}
	}
	// Defaults: disabled, keep 5, no destination.
	cfg, errs := FromAPI(apiSettings(t, `{"admin":{"bindAddress":"127.0.0.1","port":8443},"interfaces":[]}`), Defaults())
	if len(errs) != 0 || cfg.Backup.Schedule.Frequency != "disabled" || cfg.Backup.Retention != 5 || cfg.Backup.Destination != nil {
		t.Errorf("defaults: %+v %v", cfg.Backup, errs)
	}
}

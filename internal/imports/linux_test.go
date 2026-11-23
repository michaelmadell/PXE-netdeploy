package imports

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func isoFixture(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "media", "iso9660", "testdata", name+".iso.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, _ := gzip.NewReader(f)
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The ISO 9660 fixture has casper/, .disk/info and EFI/BOOT/BOOTX64.EFI, so
// it imports as Ubuntu media, and keeps its ISO though none was asked for.
func TestImportLinuxMedia(t *testing.T) {
	h := newHarness(t)
	iso := isoFixture(t, "rr-joliet")
	u := h.upload(t, "ubuntu.iso", iso)
	im, _ := h.importAndWait(t, u, false, store.JobSucceeded)
	if *im.Kind != "linux" || *im.VolumeLabel != "NDTEST_RRJOLIET" || !im.RetainISO || im.ISOBlob == nil || *im.ISOBlob != *u.SHA256 {
		t.Fatalf("image: %+v", im)
	}
	var d api.LinuxImageDetails
	if err := json.Unmarshal(im.Details, &d); err != nil {
		t.Fatal(err)
	}
	if d.Family != api.LinuxFamilyUbuntu || *d.Distribution != "Ubuntu-Server" || *d.Version != "26.04.1" || d.Architecture != "x64" || !d.Deployable || d.DeployableReason != nil {
		t.Errorf("details: %s", im.Details)
	}
	if d.Boot.Shim == nil || d.Boot.Shim.SourcePath != "EFI/BOOT/BOOTX64.EFI" || d.Boot.Shim.Signer != "unknown" || d.Boot.Shim.Signers == nil || len(*d.Boot.Shim.Signers) != 0 {
		t.Errorf("shim: %+v", d.Boot.Shim)
	}
	if d.Boot.Kernel.SourcePath != "casper/vmlinuz" || d.Boot.Kernel.Sha256 != sha("kernel") ||
		d.Boot.Initrd.SourcePath != "casper/initrd" || d.Boot.Initrd.Sha256 != sha("initrd") {
		t.Errorf("kernel/initrd: %+v", d.Boot)
	}
	if len(im.Warnings) != 1 || !strings.Contains(im.Warnings[0], "isn't signed by a Microsoft UEFI CA") {
		t.Errorf("warnings: %q", im.Warnings)
	}
	// The whole tree is there, Rock Ridge names and all.
	tree := h.content.Tree(*im.TreeID)
	if b, err := fs.ReadFile(tree, ".disk/info"); err != nil || !strings.HasPrefix(string(b), "Ubuntu-Server") {
		t.Errorf(".disk/info: %q %v", b, err)
	}
}

// Plain ISO 9660 names (8.3, upper case) still import.
func TestImportPlainISO9660(t *testing.T) {
	h := newHarness(t)
	im, _ := h.importAndWait(t, h.upload(t, "plain.iso", isoFixture(t, "plain")), false, store.JobSucceeded)
	if *im.Kind != "linux" { // CASPER/VMLINUZ and CASPER/INITRD are found ignoring case
		t.Fatalf("kind %s: %s", *im.Kind, im.Details)
	}
}

func TestDetectLinux(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	cases := []struct {
		name    string
		fs      fstest.MapFS
		family  api.LinuxFamily
		dist    string
		version string
		arch    string
		kernel  string
		warn    string
	}{
		{name: "ubuntu arm64", fs: fstest.MapFS{
			"casper/vmlinuz": file("k"), "casper/initrd": file("i"),
			".disk/info": file(`Ubuntu-Server 24.04.5 LTS "Noble Numbat" - Release arm64 (20260909)` + "\n"),
		}, family: api.LinuxFamilyUbuntu, dist: "Ubuntu-Server", version: "24.04.5", arch: "arm64", kernel: "casper/vmlinuz"},
		{name: "ubuntu, no .disk/info", fs: fstest.MapFS{
			"casper/vmlinuz": file("k"), "casper/initrd": file("i"), "EFI/BOOT/BOOTAA64.EFI": file("s"),
		}, family: api.LinuxFamilyUbuntu, arch: "arm64", kernel: "casper/vmlinuz"},
		{name: "debian", fs: fstest.MapFS{
			"install.amd/vmlinuz": file("k"), "install.amd/initrd.gz": file("i"),
			".disk/info": file(`Debian GNU/Linux 13.1.0 "Trixie" - Official amd64 NETINST with firmware 20250906-10:22`),
		}, family: api.LinuxFamilyDebian, dist: "Debian GNU/Linux", version: "13.1.0", arch: "x64", kernel: "install.amd/vmlinuz"},
		{name: "debian live", fs: fstest.MapFS{
			"install/vmlinuz": file("k"), "install/initrd.gz": file("i"), "live/vmlinuz": file("lk"), "live/initrd.img": file("li"),
			"EFI/boot/bootx64.efi": file("s"),
			".disk/info":           file("Official Debian GNU/Linux Live 13.8.0 xfce 2026-11-21T09:15:44Z"),
		}, family: api.LinuxFamilyDebian, dist: "Debian GNU/Linux Live", version: "13.8.0", arch: "x64", kernel: "install/vmlinuz"},
		{name: "rocky", fs: fstest.MapFS{
			".treeinfo":              file("[general]\nname = Rocky Linux\nversion = 10.1\narch = x86_64\n\n[images-x86_64]\nkernel = images/pxeboot/vmlinuz\ninitrd = images/pxeboot/initrd.img\n"),
			"images/pxeboot/vmlinuz": file("k"), "images/pxeboot/initrd.img": file("i"),
		}, family: api.LinuxFamilyRedhat, dist: "Rocky Linux", version: "10.1", arch: "x64", kernel: "images/pxeboot/vmlinuz"},
		{name: "rhel boot iso", fs: fstest.MapFS{
			".discinfo":              file("1793810521.107004\n10.3\nx86_64\n"),
			"images/pxeboot/vmlinuz": file("k"), "images/pxeboot/initrd.img": file("i"), "images/install.img": file("s2"),
			"EFI/BOOT/grub.cfg": file("search --no-floppy --set=root -l 'RHEL-10-3-BaseOS-x86_64'\nmenuentry 'Install Red Hat Enterprise Linux 10.3' --class fedora {\n\tlinuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=RHEL-10-3-BaseOS-x86_64 quiet\n"),
		}, family: api.LinuxFamilyRedhat, dist: "Red Hat Enterprise Linux", version: "10.3", arch: "x64", kernel: "images/pxeboot/vmlinuz"},
		{name: "install/ that isn't Debian", fs: fstest.MapFS{"install/vmlinuz": file("k"), "install/initrd.gz": file("i")}},
		{name: "casper without initrd", fs: fstest.MapFS{"casper/vmlinuz": file("k")}, warn: "casper/initrd is missing"},
		{name: "nothing", fs: fstest.MapFS{"README": file("x")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, warnings := detectLinux(c.fs)
			if c.family == "" {
				if m != nil {
					t.Fatalf("detected %+v", m)
				}
				if (c.warn == "") != (len(warnings) == 0) || (c.warn != "" && !strings.Contains(warnings[0], c.warn)) {
					t.Errorf("warnings %q", warnings)
				}
				return
			}
			if m == nil {
				t.Fatalf("not detected (%q)", warnings)
			}
			if m.family != c.family || m.distribution != c.dist || m.version != c.version || m.arch != c.arch || m.kernel != c.kernel {
				t.Errorf("got %+v", m)
			}
		})
	}
}

// TestRealLinuxISO imports real Linux media when NETDEPLOY_TEST_LINUX_ISO
// is set (a path, or several separated by the OS list separator). Phase 7
// found Ubuntu's shims signed by the 2011 UEFI CA only and its kernels by
// Canonical.
func TestRealLinuxISO(t *testing.T) {
	env := os.Getenv("NETDEPLOY_TEST_LINUX_ISO")
	if env == "" {
		t.Skip("set NETDEPLOY_TEST_LINUX_ISO to Linux ISOs")
	}
	for _, p := range filepath.SplitList(env) {
		t.Run(filepath.Base(p), func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			h := newHarness(t)
			im, _ := h.importAndWait(t, h.upload(t, filepath.Base(p), b), false, store.JobSucceeded)
			t.Logf("kind %s, label %s, warnings %q", *im.Kind, *im.VolumeLabel, im.Warnings)
			t.Logf("details %s", im.Details)
			var d api.LinuxImageDetails
			if err := json.Unmarshal(im.Details, &d); err != nil || *im.Kind != "linux" {
				t.Fatalf("not linux: %v", err)
			}
			if d.Boot.Shim == nil || len(derefSigners(d.Boot.Shim.Signers)) == 0 {
				t.Errorf("shim missing or without a Microsoft signer: %+v", d.Boot.Shim)
			}
			if d.Version == nil || d.Distribution == nil || *d.Distribution == "" {
				t.Errorf("no distribution or version: %+v", d)
			}
			// Ubuntu: deployable, ISO kept, kernel signed by Canonical.
			if d.Family == api.LinuxFamilyUbuntu {
				if !d.Deployable || !im.RetainISO {
					t.Errorf("ubuntu: deployable %v, retainIso %v", d.Deployable, im.RetainISO)
				}
				if d.Boot.Kernel.SignedBy == nil || !strings.Contains(strings.Join(*d.Boot.Kernel.SignedBy, " "), "Canonical") {
					t.Errorf("kernel signers: %+v", d.Boot.Kernel)
				}
			} else if d.Deployable || d.DeployableReason == nil {
				t.Errorf("%s: deployable %v", d.Family, d.Deployable)
			}
		})
	}
}

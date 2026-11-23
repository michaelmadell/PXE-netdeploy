package imports

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// linuxMedia is what family detection found (ADR 0033).
type linuxMedia struct {
	family       api.LinuxFamily
	distribution string
	version      string
	arch         string // "x64", "arm64", or the media's own name if neither
	description  string
	kernel       string // paths as spelled on the media
	initrd       string
}

// detectLinux recognises the families of ADR 0033 in order; nil when none
// matches. A family whose kernel or initrd is missing is reported as a
// warning (and nil), so the media imports as kind other.
func detectLinux(media fs.FS) (*linuxMedia, []string) {
	exists := func(p string) bool {
		fi, err := fs.Stat(media, p)
		return err == nil && fi.Mode().IsRegular()
	}
	diskInfo := func() string {
		b, err := fs.ReadFile(media, ".disk/info")
		if err != nil || len(b) > 4096 {
			return ""
		}
		return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	}

	// ubuntu: casper.
	if dirExists(media, "casper") {
		k, i := realPath(media, "casper/vmlinuz"), realPath(media, "casper/initrd")
		if !exists(k) || !exists(i) {
			return nil, []string{"This looks like Ubuntu media (casper/), but casper/vmlinuz or casper/initrd is missing, so it was imported as other media."}
		}
		m := &linuxMedia{family: api.LinuxFamilyUbuntu, kernel: k, initrd: i, description: diskInfo()}
		m.distribution, m.version, m.arch = parseDiskInfo(m.description)
		if m.arch == "" {
			m.arch = archFromShim(media)
		}
		return m, nil
	}

	// debian: debian-installer, in install.amd/ or install.a64/ on installer
	// media, in install/ on live media (whose live system is in live/).
	for _, d := range []struct{ dir, arch string }{{"install.amd", "x64"}, {"install.a64", "arm64"}, {"install", ""}} {
		if !dirExists(media, d.dir) {
			continue
		}
		k, i := realPath(media, d.dir+"/vmlinuz"), realPath(media, d.dir+"/initrd.gz")
		if !exists(k) || !exists(i) {
			if d.dir == "install" {
				continue // install/ alone isn't distinctive enough to warn about
			}
			return nil, []string{"This looks like Debian media (" + d.dir + "/), but its vmlinuz or initrd.gz is missing, so it was imported as other media."}
		}
		info := diskInfo()
		if d.dir == "install" && !strings.Contains(info, "Debian") {
			continue
		}
		m := &linuxMedia{family: api.LinuxFamilyDebian, kernel: k, initrd: i, arch: d.arch, description: info}
		var arch string
		m.distribution, m.version, arch = parseDiskInfo(strings.TrimPrefix(info, "Official "))
		if m.arch == "" {
			m.arch = arch
		}
		if m.arch == "" {
			m.arch = archFromShim(media)
		}
		return m, nil
	}

	// redhat: Anaconda's .treeinfo (DVDs, trees), else .discinfo with
	// images/pxeboot (boot ISOs, which have no .treeinfo).
	if b, err := fs.ReadFile(media, ".treeinfo"); err == nil && len(b) < 1<<20 {
		ti := parseINI(b)
		m := &linuxMedia{family: api.LinuxFamilyRedhat}
		for _, sec := range []string{"general", "release"} {
			if v := ti[sec]["name"]; v != "" && m.distribution == "" {
				m.distribution = v
			}
			if v := ti[sec]["version"]; v != "" && m.version == "" {
				m.version = v
			}
		}
		rawArch := ti["general"]["arch"]
		if rawArch == "" {
			rawArch = ti["tree"]["arch"]
		}
		m.arch = linuxArch(rawArch)
		img := ti["images-"+rawArch]
		m.kernel, m.initrd = realPath(media, img["kernel"]), realPath(media, img["initrd"])
		if img["kernel"] == "" || img["initrd"] == "" || !exists(m.kernel) || !exists(m.initrd) {
			return nil, []string{"This looks like Red Hat family media (.treeinfo), but the kernel or initrd it names for " + rawArch + " is missing, so it was imported as other media."}
		}
		m.description = strings.TrimSpace(m.distribution + " " + m.version)
		return m, nil
	}
	if b, err := fs.ReadFile(media, ".discinfo"); err == nil && len(b) < 4096 {
		k, i := realPath(media, "images/pxeboot/vmlinuz"), realPath(media, "images/pxeboot/initrd.img")
		if !exists(k) || !exists(i) {
			return nil, []string{"This looks like Red Hat family media (.discinfo), but images/pxeboot/vmlinuz or initrd.img is missing, so it was imported as other media."}
		}
		// .discinfo: a timestamp, the version, the architecture.
		lines := strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n")
		m := &linuxMedia{family: api.LinuxFamilyRedhat, kernel: k, initrd: i}
		if len(lines) > 1 {
			m.version = strings.TrimSpace(lines[1])
		}
		if len(lines) > 2 {
			m.arch = linuxArch(strings.TrimSpace(lines[2]))
		}
		m.distribution = grubDistribution(media)
		m.description = strings.TrimSpace(m.distribution + " " + m.version)
		return m, nil
	}
	return nil, nil
}

var installEntry = regexp.MustCompile(`menuentry '(?:Install|Test this media & install) (.+?) \d[\d.]*'`)

// grubDistribution reads the distribution's name from the first
// "Install <name> <version>" entry of the media's GRUB configuration, as
// written by lorax (Fedora, RHEL and its rebuilds); "" if there's none.
func grubDistribution(media fs.FS) string {
	for _, p := range []string{"EFI/BOOT/grub.cfg", "boot/grub2/grub.cfg"} {
		b, err := fs.ReadFile(media, realPath(media, p))
		if err != nil || len(b) > 1<<20 {
			continue
		}
		if m := installEntry.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}

func dirExists(media fs.FS, p string) bool {
	fi, err := fs.Stat(media, p)
	return err == nil && fi.IsDir()
}

var versionToken = regexp.MustCompile(`^\d+(\.\d+)*`)

// parseDiskInfo splits a .disk/info line such as
// `Ubuntu-Server 26.04.1 LTS "Resolute Raccoon" - Release amd64 (20260826)`
// or `Debian GNU/Linux 13.1.0 "Trixie" - Official amd64 NETINST …` into the
// distribution (the words before the version), the version and the
// architecture.
func parseDiskInfo(s string) (dist, version, arch string) {
	words := strings.Fields(s)
	for i, w := range words {
		if version == "" && versionToken.MatchString(w) {
			dist, version = strings.Join(words[:i], " "), versionToken.FindString(w)
			continue
		}
		if version != "" && arch == "" {
			if a := linuxArch(w); a == "x64" || a == "arm64" {
				arch = a
			}
		}
	}
	return dist, version, arch
}

// linuxArch maps a distribution's architecture name to the API's.
func linuxArch(s string) string {
	switch strings.ToLower(s) {
	case "amd64", "x86_64":
		return "x64"
	case "arm64", "aarch64":
		return "arm64"
	}
	return s
}

func archFromShim(media fs.FS) string {
	if _, err := fs.Stat(media, realPath(media, "EFI/BOOT/BOOTAA64.EFI")); err == nil {
		return "arm64"
	}
	if _, err := fs.Stat(media, realPath(media, "EFI/BOOT/BOOTX64.EFI")); err == nil {
		return "x64"
	}
	return ""
}

// parseINI reads .treeinfo: sections of key = value lines; names lower-cased.
func parseINI(b []byte) map[string]map[string]string {
	out := map[string]map[string]string{}
	sec := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			sec = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
		default:
			if k, v, ok := strings.Cut(line, "="); ok {
				if out[sec] == nil {
					out[sec] = map[string]string{}
				}
				out[sec][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// linux builds the details of detected Linux media: the shim, GRUB and
// kernel with every signer, the initrd, and the warnings.
func (im *Images) linux(media fs.FS, m *linuxMedia, shas map[string]string) (api.LinuxImageDetails, []string, error) {
	var warnings []string
	if m.arch != "x64" && m.arch != "arm64" {
		return api.LinuxImageDetails{}, nil, unsupported(fmt.Sprintf("This %s media is for the %q architecture; only x64 and arm64 are supported.", m.family, m.arch))
	}
	d := api.LinuxImageDetails{Kind: api.LinuxImageDetailsKindLinux, Family: m.family, Architecture: api.Architecture(m.arch)}
	if m.distribution != "" {
		d.Distribution = &m.distribution
	}
	if m.version != "" {
		d.Version = &m.version
	}
	if m.description != "" {
		d.Description = &m.description
	}
	efi := map[string][2]string{"x64": {"BOOTX64.EFI", "grubx64.efi"}, "arm64": {"BOOTAA64.EFI", "grubaa64.efi"}}[m.arch]
	bm := func(p string) (*api.BootManager, error) {
		p = realPath(media, p)
		b, err := fs.ReadFile(media, p)
		if err != nil {
			return nil, err
		}
		x := im.bootManager(p, shas[cas.Fold(p)], b, m.arch)
		out := apiBootManagers([]store.BootManager{x})[0]
		return &out, nil
	}
	shim, err := bm("EFI/BOOT/" + efi[0])
	if err != nil {
		warnings = append(warnings, "No EFI/BOOT/"+efi[0]+" (the distribution's shim) was found: this media can't be booted under Secure Boot.")
	} else {
		d.Boot.Shim = shim
		ss := derefSigners(shim.Signers)
		switch {
		case len(ss) == 0:
			warnings = append(warnings, "The shim ("+shim.SourcePath+") isn't signed by a Microsoft UEFI CA: Secure Boot will refuse it.")
		case slices.Contains(ss, api.SecureBootSignerUefiCa2011) && !slices.Contains(ss, api.SecureBootSignerUefiCa2023):
			warnings = append(warnings, "The shim is signed by the Microsoft UEFI CA 2011 only: machines that trust only the 2023 CAs will refuse it.")
		}
	}
	if g, err := bm("EFI/BOOT/" + efi[1]); err == nil {
		d.Boot.Grub = g
	}
	k, err := bm(m.kernel)
	if err != nil {
		return d, nil, invalid(m.kernel + ": " + err.Error())
	}
	d.Boot.Kernel = *k
	d.Boot.Initrd.SourcePath = m.initrd
	d.Boot.Initrd.Sha256 = shas[cas.Fold(m.initrd)]

	d.Deployable = m.family == api.LinuxFamilyUbuntu
	if !d.Deployable {
		reason := fmt.Sprintf("%s media is recognised but can't be deployed yet: only Ubuntu has been proven (ADR 0033).", familyName(m.family))
		d.DeployableReason = &reason
	}
	return d, warnings, nil
}

func familyName(f api.LinuxFamily) string {
	switch f {
	case api.LinuxFamilyDebian:
		return "Debian"
	case api.LinuxFamilyRedhat:
		return "Red Hat family"
	}
	return "Ubuntu"
}

func derefSigners(p *[]api.SecureBootSigner) []api.SecureBootSigner {
	if p == nil {
		return nil
	}
	return *p
}

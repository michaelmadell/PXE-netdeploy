package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// linuxImage reads a Linux image's details (ADR 0033). reason says why the
// image can't be deployed by a Linux profile ("" when it can).
func linuxImage(im store.Image) (api.LinuxImageDetails, string) {
	var d api.LinuxImageDetails
	if im.Status != store.ImageReady || im.Kind == nil || *im.Kind != "linux" {
		return d, fmt.Sprintf("Image %q is not a ready Linux image.", im.Name)
	}
	if err := json.Unmarshal(im.Details, &d); err != nil {
		return d, fmt.Sprintf("Image %q has unreadable details: %v.", im.Name, err)
	}
	if !d.Deployable {
		reason := fmt.Sprintf("Image %q can't be deployed yet.", im.Name)
		if d.DeployableReason != nil {
			reason = fmt.Sprintf("Image %q: %s", im.Name, *d.DeployableReason)
		}
		return d, reason
	}
	if d.Family == api.LinuxFamilyUbuntu && im.ISOBlob == nil {
		return d, fmt.Sprintf("Image %q has no kept ISO, which the Ubuntu installer downloads; import it again.", im.Name)
	}
	return d, ""
}

// validateLinuxProfile adds a Linux profile's findings (ADR 0034) to fs,
// which holds checkProfile's.
func (s *Server) validateLinuxProfile(ctx context.Context, p store.Profile, fs []finding) ([]finding, error) {
	add := func(sev, code, format string, a ...any) {
		fs = append(fs, finding{sev, code, fmt.Sprintf(format, a...)})
	}
	im, err := s.store.Image(ctx, p.InstallImageID)
	if err != nil {
		return fs, nil // checkProfile reported it
	}
	d, reason := linuxImage(im)
	if reason != "" {
		return fs, nil
	}
	if d.Boot.Shim == nil {
		add("error", "no_shim", "Image %q has no shim (EFI/BOOT/BOOTX64.EFI or BOOTAA64.EFI): it can't boot under Secure Boot through netdeploy.", im.Name)
	} else {
		var signers []api.SecureBootSigner
		if d.Boot.Shim.Signers != nil {
			signers = *d.Boot.Shim.Signers
		}
		has11, has23 := slices.Contains(signers, api.SecureBootSignerUefiCa2011), slices.Contains(signers, api.SecureBootSignerUefiCa2023)
		switch {
		case !has11 && !has23:
			add("error", "shim_not_microsoft_signed", "Image %q's shim isn't signed by a Microsoft UEFI CA: Secure Boot will refuse it.", im.Name)
		case !has23:
			add("warning", "shim_2011_only", "Image %q's shim is signed by the Microsoft UEFI CA 2011 only: machines that trust only the 2023 CAs refuse it.", im.Name)
		case !has11:
			add("warning", "shim_2023_only", "Image %q's shim is signed by the Microsoft UEFI CA 2023 only: machines without that CA refuse it.", im.Name)
		}
	}
	add("info", "http_boot_only", "Linux profiles boot through HTTP(S) Boot only; PXE clients get no answer (Phase 7 decision 6).")
	if s.settings != nil {
		cfg, _ := s.settings.Current()
		if !cfg.Architectures.HTTPBoot {
			add("error", "http_boot_off", "HTTP Boot is turned off in Settings (architectures.httpBoot): Linux profiles can't boot anything.")
		}
		if d.Family == api.LinuxFamilyUbuntu && len(s.settings.Endpoints(false)) == 0 {
			add("error", "linux_needs_http", "No deployment interface serves plain HTTP: the Ubuntu installer downloads its ISO over HTTP only (it has no TLS client). Turn HTTP on for the interface in Settings.")
		}
	}
	if !s.ipxe.Has(string(d.Architecture)) {
		add("error", "http_boot_unavailable", "The iPXE chain for %s isn't installed (capabilities.httpBoot).", d.Architecture)
	}
	return fs, nil
}

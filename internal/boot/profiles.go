package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// PXEBootFile is the boot file PXE clients get: the boot manager at a
// boot image tree's root (ADR 0022).
const PXEBootFile = "bootmgfw.efi"

// Decider is a Booter that also knows the default content (the default
// profile's boot image), for clients nothing tells apart.
type Decider interface {
	Booter
	Default(ctx context.Context, arch Arch) (Answer, bool, string)
}

// Profiles decides what a client boots from deployments and profiles
// (ADR 0013, 0024): an armed deployment's profile, else the client's
// profile, else the default profile.
type Profiles struct {
	Store *store.Store
	// Tree serves a content-store tree (cas.Store.Tree).
	Tree func(id string) fs.FS
	// DefaultProfileID returns bootPrompt.defaultProfileId ("" for none).
	DefaultProfileID func() string
}

// Boot implements Booter.
func (p *Profiles) Boot(ctx context.Context, r Request) (Answer, bool, string) {
	c, err := p.Store.ClientByMAC(ctx, r.MAC)
	switch {
	case err == nil:
		if d, err := p.Store.ActiveDeployment(ctx, c.ID); err == nil && d.Status == "pending" {
			return p.answer(ctx, r.Arch, snapshotBootImage(d), d.ProfileID, "deployment")
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Answer{}, false, "reading deployments: " + err.Error()
		}
		if c.ProfileID != nil {
			return p.answer(ctx, r.Arch, "", *c.ProfileID, "client profile")
		}
	case !errors.Is(err, store.ErrNotFound):
		return Answer{}, false, "reading the client: " + err.Error()
	}
	return p.Default(ctx, r.Arch)
}

// Default answers with the default profile's boot image.
func (p *Profiles) Default(ctx context.Context, arch Arch) (Answer, bool, string) {
	id := ""
	if p.DefaultProfileID != nil {
		id = p.DefaultProfileID()
	}
	if id == "" {
		return Answer{}, false, "no profile to offer (the client has none and bootPrompt.defaultProfileId is unset)"
	}
	return p.answer(ctx, arch, "", id, "default profile")
}

// snapshotBootImage is the boot image an armed deployment's profile
// snapshot names, if any.
func snapshotBootImage(d store.Deployment) string {
	var snap struct {
		BootImageID string `json:"bootImageId"`
	}
	_ = json.Unmarshal(d.ProfileSnapshot, &snap)
	return snap.BootImageID
}

// answer serves bootImageID (or the profile's) to a client of arch. No
// fallback when it can't (ADR 0024): the reason says why.
func (p *Profiles) answer(ctx context.Context, arch Arch, bootImageID, profileID, source string) (Answer, bool, string) {
	if bootImageID == "" {
		pr, err := p.Store.Profile(ctx, profileID)
		if err != nil {
			return Answer{}, false, fmt.Sprintf("%s %s: %v", source, profileID, err)
		}
		bootImageID = pr.BootImageID
	}
	bi, err := p.Store.BootImage(ctx, bootImageID)
	if err != nil {
		return Answer{}, false, fmt.Sprintf("%s %s: boot image %s: %v", source, profileID, bootImageID, err)
	}
	if bi.TreeID == nil {
		return Answer{}, false, fmt.Sprintf("%s %s: boot image %q has never been built", source, profileID, bi.Name)
	}
	want := map[Arch]string{ArchUEFIX64: "x64", ArchUEFIArm64: "arm64"}[arch]
	switch {
	case arch.HTTP():
		return Answer{}, false, "HTTP Boot isn't supported yet (no HTTP Boot ISOs)"
	case want == "":
		return Answer{}, false, "no boot images for " + string(arch) + " clients"
	case want != bi.Architecture:
		return Answer{}, false, fmt.Sprintf("%s %s: boot image %q is %s, the client is %s", source, profileID, bi.Name, bi.Architecture, arch)
	}
	return Answer{File: PXEBootFile, Root: p.Tree(*bi.TreeID), TreeID: *bi.TreeID,
		BootImageID: bi.ID, ProfileID: profileID, Source: source}, true, ""
}

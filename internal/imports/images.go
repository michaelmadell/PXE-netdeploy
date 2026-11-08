// Package imports holds the import job kinds: ISO images (ADR 0018) and
// driver packages (ADR 0019).
package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/signer"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/udf"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/wim"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
)

// KindImageImport is the image import job kind.
const KindImageImport = "image.import"

// Boot managers looked for inside each boot.wim image.
var wimBootManagers = []string{"Windows/Boot/EFI/bootmgfw.efi", "Windows/Boot/EFI_EX/bootmgfw_EX.efi"}

// Images imports ISOs.
type Images struct {
	st      *store.Store
	content *cas.Store
	uploads *uploads.Manager
	signers *signer.Classifier
	events  *events.Bus
	log     *slog.Logger
}

// ImagesOptions configure Images.
type ImagesOptions struct {
	Store   *store.Store
	Content *cas.Store
	Uploads *uploads.Manager
	Signers *signer.Classifier // nil: signer.Default()
	Events  *events.Bus
	Logger  *slog.Logger
}

// NewImages returns the image importer.
func NewImages(o ImagesOptions) *Images {
	if o.Signers == nil {
		o.Signers = signer.Default()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Images{st: o.Store, content: o.Content, uploads: o.Uploads, signers: o.Signers, events: o.Events, log: o.Logger}
}

// Kind is the job kind to register with the runner.
func (im *Images) Kind() jobs.Kind {
	return jobs.Kind{Name: KindImageImport, Lane: jobs.LaneImport, Run: im.run, Abandon: im.abandon}
}

func (im *Images) run(ctx context.Context, r *jobs.Run) error {
	var p params
	if err := r.Params(&p); err != nil || r.Job.ResourceID == nil {
		return fmt.Errorf("bad job parameters: %v", err)
	}
	imageID := *r.Job.ResourceID
	err := im.importISO(ctx, r, imageID, p.UploadID)
	if err == nil {
		if _, rerr := im.uploads.Release(context.WithoutCancel(ctx), p.UploadID, r.Job.ID, true); rerr != nil {
			r.Logger().Warn("release upload", "upload_id", p.UploadID, "err", rerr)
		}
		im.publish(imageID)
		return nil
	}
	if ctx.Err() != nil && !r.CancelRequested() {
		// Shutting down: Abandon records it as interrupted.
		return err
	}
	if ctx.Err() != nil {
		err = cancelledError()
	}
	return im.fail(context.WithoutCancel(ctx), r.Job.ID, imageID, p.UploadID, err)
}

// fail records the failure on the image, saying until when the upload is
// kept for a retry, and returns the job's error with the same text.
func (im *Images) fail(ctx context.Context, jobID, imageID, uploadID string, cause error) error {
	e := failure(ctx, im.uploads, jobID, uploadID, cause)
	if err := im.st.FailImage(ctx, imageID, jobs.Problem(e)); err != nil {
		im.log.Error("record image failure", "image_id", imageID, "err", err)
	}
	im.publish(imageID)
	return e
}

func (im *Images) abandon(ctx context.Context, j store.Job, reason string) error {
	var p params
	_ = json.Unmarshal(j.Params, &p)
	if j.ResourceID == nil {
		return nil
	}
	im.fail(ctx, j.ID, *j.ResourceID, p.UploadID, abandoned(reason))
	return nil
}

func (im *Images) publish(imageID string) {
	im.events.Publish(events.Event{Type: events.ImageUpdated, ResourceID: imageID})
}

// Media errors.
func unsupported(detail string) error {
	return jobs.Fail(http.StatusUnprocessableEntity, "unsupported_media", "Unsupported media", detail)
}

func invalid(detail string) error {
	return jobs.Fail(http.StatusUnprocessableEntity, "invalid_media", "Invalid media", detail)
}

func (im *Images) importISO(ctx context.Context, r *jobs.Run, imageID, uploadID string) error {
	log := r.Logger()
	up, err := im.st.Upload(ctx, uploadID)
	if err != nil {
		return fmt.Errorf("upload %s: %w", uploadID, err)
	}
	p, err := im.uploads.Path(uploadID)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("open upload: %w", err)
	}
	defer f.Close()

	r.Progress(0, "Reading the ISO")
	media, err := udf.Open(f, up.Length)
	switch {
	case errors.Is(err, udf.ErrNotUDF):
		return unsupported("This ISO has no UDF file system. Windows install media is UDF; other media (e.g. Linux ISO9660) is not supported yet.")
	case errors.Is(err, udf.ErrUnsupported):
		return unsupported(err.Error())
	case err != nil:
		return invalid(err.Error())
	}

	type file struct {
		path string
		size int64
	}
	var files []file
	var total int64
	err = fs.WalkDir(media, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			files = append(files, file{p, info.Size()})
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return invalid(err.Error())
	}
	if err := im.content.CheckSpace(total); err != nil {
		return err
	}

	st, err := im.content.NewStaging(r.Job.ID)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			st.Discard()
		}
	}()

	// Copy every file into the content store (95% of the progress).
	shas := map[string]string{} // folded path -> sha256
	var done int64
	for _, fl := range files {
		src, err := media.Open(fl.path)
		if err != nil {
			return invalid(err.Error())
		}
		pr := &progressReader{ctx: ctx, r: src, done: &done, total: total, run: r, msg: "Copying " + fl.path}
		sha, _, err := st.Add(fl.path, pr)
		src.Close()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, udf.ErrCorrupt) {
				return invalid(err.Error())
			}
			return err
		}
		shas[cas.Fold(fl.path)] = sha
	}
	log.Info("image files copied", "files", len(files), "bytes", total)

	r.Progress(96, "Reading boot managers and editions")
	res := store.ImportResult{VolumeLabel: media.Label()}
	var details any
	installPath := ""
	for _, name := range []string{"sources/install.wim", "sources/install.esd", "sources/install.swm"} {
		if _, err := fs.Stat(media, name); err == nil {
			installPath = name
			break
		}
	}
	_, bootErr := fs.Stat(media, "sources/boot.wim")
	if installPath != "" && bootErr == nil {
		res.Kind = "windows"
		d, bms, warnings, err := im.windows(ctx, media, st, shas, installPath)
		if err != nil {
			return err
		}
		details, res.BootManagers, res.Warnings = d, bms, warnings
	} else {
		res.Kind = "other"
		bms := im.mediaBootManagers(media, shas)
		res.BootManagers = bms
		other := api.OtherImageDetails{Kind: api.OtherImageDetailsKindOther}
		if len(bms) > 0 {
			files := apiBootManagers(bms)
			other.BootFiles = &files
		}
		details = other
	}
	if res.Details, err = json.Marshal(details); err != nil {
		return err
	}

	img, err := im.st.Image(ctx, imageID)
	if err != nil {
		return err
	}
	if img.RetainISO {
		if err := st.LinkBlob(p, *up.SHA256, up.Length); err != nil {
			return err
		}
		res.ISOBlob = up.SHA256
	}

	r.Progress(99, "Saving")
	_, err = st.Commit(ctx, store.TreeImage, func(ctx context.Context, treeID string) error {
		res.TreeID = treeID
		return im.st.CompleteImageImport(ctx, imageID, res)
	})
	committed = true
	if err != nil {
		return err
	}
	r.Progress(100, "Imported ("+res.Kind+" media)")
	log.Info("image imported", "kind", res.Kind, "label", res.VolumeLabel, "boot_managers", len(res.BootManagers), "warnings", len(res.Warnings))
	return nil
}

// windows reads editions from the install image, boot image indexes from
// boot.wim, and every boot manager on the media and inside boot.wim.
func (im *Images) windows(ctx context.Context, media *udf.FS, st *cas.Staging, shas map[string]string, installPath string) (api.WindowsImageDetails, []store.BootManager, []string, error) {
	var warnings []string
	d := api.WindowsImageDetails{Kind: api.WindowsImageDetailsKindWindows}
	switch path.Ext(installPath) {
	case ".esd":
		d.InstallImageFormat = api.WindowsImageDetailsInstallImageFormatEsd
	case ".swm":
		d.InstallImageFormat = api.WindowsImageDetailsInstallImageFormatSwm
	default:
		d.InstallImageFormat = api.WindowsImageDetailsInstallImageFormatWim
	}
	installInfo, err := wimInfo(media, installPath)
	if err != nil {
		return d, nil, nil, invalid(fmt.Sprintf("%s: %v", installPath, err))
	}
	if len(installInfo.Images) == 0 {
		return d, nil, nil, invalid(installPath + " has no images")
	}
	arch := installInfo.Images[0].Arch
	if arch != "x64" && arch != "arm64" {
		return d, nil, nil, unsupported(fmt.Sprintf("%s is for the %q architecture; only x64 and arm64 are supported.", installPath, arch))
	}
	d.Architecture = api.Architecture(arch)
	for _, x := range installInfo.Images {
		e := api.WindowsEdition{Index: x.Index, Name: x.DisplayName}
		if e.Name == "" {
			e.Name = x.Name
		}
		if x.EditionID != "" {
			e.EditionId = &x.EditionID
		}
		if x.Build != "" {
			e.Build = &x.Build
		}
		if x.Arch == "x64" || x.Arch == "arm64" {
			a := api.Architecture(x.Arch)
			e.Architecture = &a
		}
		if len(x.Languages) > 0 {
			langs := x.Languages
			e.Languages = &langs
		}
		if x.DefaultLanguage != "" {
			e.DefaultLanguage = &x.DefaultLanguage
		}
		if x.TotalBytes > 0 {
			n := int(x.TotalBytes)
			e.SizeBytes = &n
		}
		d.Editions = append(d.Editions, e)
	}

	bms := im.mediaBootManagers(media, shas)

	bootFile, err := media.Open("sources/boot.wim")
	if err != nil {
		return d, nil, nil, invalid(err.Error())
	}
	defer bootFile.Close()
	bootInfo, _ := bootFile.Stat()
	bootRA := bootFile.(io.ReaderAt)
	info, err := wim.ReadInfo(bootRA)
	if err != nil {
		return d, nil, nil, invalid("sources/boot.wim: " + err.Error())
	}
	for _, x := range info.Images {
		idx, name := x.Index, x.Name
		d.BootImageIndexes = append(d.BootImageIndexes, struct {
			Index *int    `json:"index,omitempty"`
			Name  *string `json:"name,omitempty"`
		}{&idx, &name})
	}
	wr, err := wim.Open(bootRA, bootInfo.Size())
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("sources/boot.wim couldn't be read (%v), so boot managers inside it weren't found.", err))
	} else {
		for _, x := range info.Images {
			for _, name := range wimBootManagers {
				if ctx.Err() != nil {
					return d, nil, nil, ctx.Err()
				}
				b, err := wr.ReadFile(x.Index, name)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("boot.wim:%d/%s couldn't be read: %v", x.Index, name, err))
					continue
				}
				sha, _, err := st.AddBlob(bytes.NewReader(b))
				if err != nil {
					return d, nil, nil, err
				}
				bm := store.BootManager{SourcePath: fmt.Sprintf("boot.wim:%d/%s", x.Index, name), SHA256: sha, Signer: im.classify(b)}
				if x.Arch == "x64" || x.Arch == "arm64" {
					a := x.Arch
					bm.Architecture = &a
				}
				bms = append(bms, bm)
			}
		}
	}
	has2023 := false
	for _, b := range bms {
		if b.Signer == string(signer.WindowsUEFICA2023) {
			has2023 = true
		}
	}
	if !has2023 {
		warnings = append(warnings, "No boot manager signed by the Windows UEFI CA 2023 was found: machines that have revoked the 2011 CA can't boot this media.")
	}
	d.BootManagers = apiBootManagers(bms)
	return d, bms, warnings, nil
}

// mediaBootManagers classifies the EFI binaries in efi/boot on the media.
func (im *Images) mediaBootManagers(media *udf.FS, shas map[string]string) []store.BootManager {
	dir := realPath(media, "efi/boot")
	ents, err := fs.ReadDir(media, dir)
	if err != nil {
		return nil
	}
	var out []store.BootManager
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".efi") {
			continue
		}
		p := dir + "/" + e.Name()
		b, err := fs.ReadFile(media, p)
		if err != nil {
			continue
		}
		bm := store.BootManager{SourcePath: p, SHA256: shas[cas.Fold(p)], Signer: im.classify(b)}
		switch strings.ToLower(e.Name()) {
		case "bootx64.efi":
			a := "x64"
			bm.Architecture = &a
		case "bootaa64.efi":
			a := "arm64"
			bm.Architecture = &a
		}
		out = append(out, bm)
	}
	return out
}

// realPath returns name with each component spelled as on the media (the
// reader matches case-insensitively); name itself if it isn't found.
func realPath(media fs.FS, name string) string {
	cur := "."
	for _, part := range strings.Split(name, "/") {
		ents, err := fs.ReadDir(media, cur)
		if err != nil {
			return name
		}
		match := ""
		for _, e := range ents {
			if strings.EqualFold(e.Name(), part) {
				match = e.Name()
				break
			}
		}
		if match == "" {
			return name
		}
		cur = path.Join(cur, match)
	}
	return cur
}

func (im *Images) classify(b []byte) string {
	res, err := im.signers.Classify(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return string(signer.Unknown)
	}
	return string(res.Signer)
}

func apiBootManagers(bms []store.BootManager) []api.BootManager {
	out := make([]api.BootManager, len(bms))
	for i, b := range bms {
		out[i] = api.BootManager{SourcePath: b.SourcePath, Sha256: b.SHA256, Signer: api.SecureBootSigner(b.Signer)}
		if b.Architecture != nil {
			a := api.Architecture(*b.Architecture)
			out[i].Architecture = &a
		}
	}
	return out
}

func wimInfo(media *udf.FS, name string) (wim.Info, error) {
	f, err := media.Open(name)
	if err != nil {
		return wim.Info{}, err
	}
	defer f.Close()
	return wim.ReadInfo(f.(io.ReaderAt))
}

// progressReader reports copy progress and stops on cancellation.
type progressReader struct {
	ctx   context.Context
	r     io.Reader
	done  *int64
	total int64
	run   *jobs.Run
	msg   string
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	*p.done += int64(n)
	if p.total > 0 {
		p.run.Progress(int(*p.done*95/p.total), p.msg)
	}
	return n, err
}

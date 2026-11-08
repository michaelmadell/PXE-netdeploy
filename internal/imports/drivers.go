package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/archive"
	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/catalog"
	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/inf"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
)

// KindDriverImport is the driver package import job kind.
const KindDriverImport = "driverPackage.import"

// Drivers imports driver archives (ADR 0019).
type Drivers struct {
	st       *store.Store
	content  *cas.Store
	uploads  *uploads.Manager
	verifier *catalog.Verifier
	lzx      *archive.Tool
	events   *events.Bus
	log      *slog.Logger
}

// DriversOptions configure Drivers.
type DriversOptions struct {
	Store    *store.Store
	Content  *cas.Store
	Uploads  *uploads.Manager
	Verifier *catalog.Verifier // nil: catalog.Default()
	LZXTool  *archive.Tool     // nil: looked up when needed
	Events   *events.Bus
	Logger   *slog.Logger
}

// NewDrivers returns the driver importer.
func NewDrivers(o DriversOptions) *Drivers {
	if o.Verifier == nil {
		o.Verifier = catalog.Default()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Drivers{st: o.Store, content: o.Content, uploads: o.Uploads, verifier: o.Verifier, lzx: o.LZXTool, events: o.Events, log: o.Logger}
}

// Kind is the job kind to register with the runner.
func (d *Drivers) Kind() jobs.Kind {
	return jobs.Kind{Name: KindDriverImport, Lane: jobs.LaneImport, Run: d.run, Abandon: d.abandon}
}

func (d *Drivers) run(ctx context.Context, r *jobs.Run) error {
	var p params
	if err := r.Params(&p); err != nil || r.Job.ResourceID == nil {
		return fmt.Errorf("bad job parameters: %v", err)
	}
	pkgID := *r.Job.ResourceID
	err := d.importArchive(ctx, r, pkgID, p.UploadID)
	if err == nil {
		if _, rerr := d.uploads.Release(context.WithoutCancel(ctx), p.UploadID, r.Job.ID, true); rerr != nil {
			r.Logger().Warn("release upload", "upload_id", p.UploadID, "err", rerr)
		}
		d.publish(pkgID)
		return nil
	}
	if ctx.Err() != nil && !r.CancelRequested() {
		return err // shutting down: Abandon records it
	}
	if ctx.Err() != nil {
		err = cancelledError()
	}
	return d.fail(context.WithoutCancel(ctx), r.Job.ID, pkgID, p.UploadID, err)
}

func (d *Drivers) fail(ctx context.Context, jobID, pkgID, uploadID string, cause error) error {
	e := failure(ctx, d.uploads, jobID, uploadID, cause)
	if err := d.st.FailDriverPackage(ctx, pkgID, jobs.Problem(e)); err != nil {
		d.log.Error("record driver package failure", "package_id", pkgID, "err", err)
	}
	d.publish(pkgID)
	return e
}

func (d *Drivers) abandon(ctx context.Context, j store.Job, reason string) error {
	var p params
	_ = json.Unmarshal(j.Params, &p)
	if j.ResourceID != nil {
		d.fail(ctx, j.ID, *j.ResourceID, p.UploadID, abandoned(reason))
	}
	return nil
}

func (d *Drivers) publish(id string) {
	d.events.Publish(events.Event{Type: events.DriverUpdated, ResourceID: id})
}

func archiveError(err error) error {
	switch {
	case errors.Is(err, archive.ErrInstaller), errors.Is(err, archive.ErrUnsupported):
		return jobs.Fail(http.StatusUnprocessableEntity, "unsupported_archive", "Unsupported archive", err.Error())
	case errors.Is(err, archive.ErrInvalid), errors.Is(err, cas.ErrBadPath), errors.Is(err, store.ErrDuplicatePath):
		return jobs.Fail(http.StatusUnprocessableEntity, "invalid_archive", "Invalid archive", err.Error())
	}
	return err
}

func (d *Drivers) importArchive(ctx context.Context, r *jobs.Run, pkgID, uploadID string) error {
	up, err := d.st.Upload(ctx, uploadID)
	if err != nil {
		return fmt.Errorf("upload %s: %w", uploadID, err)
	}
	p, err := d.uploads.Path(uploadID)
	if err != nil {
		return err
	}
	st, err := d.content.NewStaging(r.Job.ID)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			st.Discard()
		}
	}()

	// Extract (80% of the progress; the expanded size isn't known ahead,
	// so progress approaches 80% as bytes arrive).
	var files int
	var extracted int64
	r.Progress(0, "Extracting")
	err = archive.Extract(ctx, p, up.Length, archive.Options{Limits: archive.DefaultLimits(up.Length), TempDir: st.Dir(), LZXTool: d.lzx},
		func(name string, src io.Reader) error {
			_, n, err := st.Add(name, src)
			files++
			extracted += n
			r.Progress(int(80*extracted/(extracted+up.Length+1)), fmt.Sprintf("Extracting (%d files)", files))
			return err
		})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return archiveError(err)
	}

	r.Progress(85, "Reading INFs and checking signatures")
	cats := map[string]*catResult{}
	var infs []store.NewDriverINF
	var warnings []string
	for _, fp := range st.Paths() {
		if !strings.EqualFold(path.Ext(fp), ".inf") {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		di, w, ok := d.analyze(st, fp, cats)
		warnings = append(warnings, w...)
		if ok {
			infs = append(infs, di)
		}
	}
	if len(infs) == 0 {
		return jobs.Fail(http.StatusUnprocessableEntity, "no_drivers", "No drivers",
			fmt.Sprintf("The archive (%d files) contains no driver INF.", files))
	}

	r.Progress(95, "Saving")
	_, err = st.Commit(ctx, store.TreeDriverPackage, func(ctx context.Context, treeID string) error {
		return d.st.CompleteDriverImport(ctx, pkgID, treeID, warnings, infs)
	})
	committed = true
	if err != nil {
		return err
	}
	r.Progress(100, fmt.Sprintf("Imported %d INFs from %d files", len(infs), files))
	r.Logger().Info("driver package imported", "files", files, "infs", len(infs), "warnings", len(warnings))
	return nil
}

type catResult struct {
	cat *catalog.Catalog
	res catalog.Result
}

// role maps an INF class to the API's DriverRole.
func role(class string) string {
	switch strings.ToLower(class) {
	case "net":
		return "network"
	case "scsiadapter", "hdc":
		return "storage"
	}
	return "other"
}

// analyze reads one INF, its catalog and the files it lists.
func (d *Drivers) analyze(st *cas.Staging, infPath string, cats map[string]*catResult) (store.NewDriverINF, []string, bool) {
	var warnings []string
	b, err := readStaged(st, infPath, inf.MaxSize)
	if err != nil {
		return store.NewDriverINF{}, []string{fmt.Sprintf("%s: %v", infPath, err)}, false
	}
	drv, _, err := inf.Read(b)
	if err != nil {
		if errors.Is(err, inf.ErrNotINF) {
			return store.NewDriverINF{}, nil, false // e.g. an autorun.inf
		}
		return store.NewDriverINF{}, []string{fmt.Sprintf("%s: %v", infPath, err)}, false
	}
	out := store.NewDriverINF{
		Path: infPath, Class: drv.Class, Role: role(drv.Class), Provider: drv.Provider, Version: drv.Version, DriverDate: drv.Date,
		Architectures: drv.Architectures, TargetOSVersions: drv.TargetOSVersions, HardwareIDs: drv.HardwareIDs,
		Enabled: len(drv.Architectures) > 0,
	}
	if out.Class == "" {
		out.Class = "Unknown"
	}
	if !out.Enabled {
		warnings = append(warnings, fmt.Sprintf("%s supports neither x64 nor arm64; it is disabled.", infPath))
	}

	dir := path.Dir(infPath)
	catName := drv.Catalogs["x64"]
	if catName == "" {
		catName = drv.Catalogs["arm64"]
	}
	if catName == "" {
		out.Signature, out.SignatureDetail = catalog.Unsigned, "the INF names no CatalogFile"
		return out, warnings, true
	}
	catPath := path.Join(dir, strings.ReplaceAll(catName, `\`, "/"))
	cr, ok := cats[cas.Fold(catPath)]
	if !ok {
		cr = &catResult{}
		cats[cas.Fold(catPath)] = cr
		cb, err := readStaged(st, catPath, 64<<20)
		switch {
		case err != nil:
			cr.res = catalog.Result{Signature: catalog.Unsigned, Detail: catName + " is missing from the package"}
		default:
			if cr.cat, err = catalog.Parse(cb); err != nil {
				cr.res = catalog.Result{Signature: catalog.Unsigned, Detail: fmt.Sprintf("%s isn't a valid catalog: %v", catName, err)}
			} else {
				cr.res = d.verifier.Verify(cr.cat)
			}
		}
	}
	out.Signature, out.Signer, out.SignatureDetail = cr.res.Signature, cr.res.Signer, cr.res.Detail
	if cr.cat == nil || (out.Signature != catalog.WHQL && out.Signature != catalog.Vendor) {
		return out, warnings, true
	}
	// Every file the INF installs must be covered by the catalog (INF and
	// PE files enforced; others warned about).
	for _, f := range append([]string{path.Base(infPath)}, drv.Files...) {
		fp := path.Join(dir, f)
		fh, err := st.Open(fp)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s lists %s, which isn't in the package.", infPath, f))
			continue
		}
		fi, _ := fh.Stat()
		covered, err := cr.cat.Contains(fh, fi.Size())
		enforced := f == path.Base(infPath) || catalog.IsPE(fh)
		fh.Close()
		if err != nil || covered {
			continue
		}
		if enforced {
			out.Signature, out.SignatureDetail = catalog.Unsigned, fmt.Sprintf("%s is not in %s", f, catName)
			break
		}
		warnings = append(warnings, fmt.Sprintf("%s: %s isn't covered by %s in a form we can check.", infPath, f, catName))
	}
	return out, warnings, true
}

func readStaged(st *cas.Staging, p string, limit int64) ([]byte, error) {
	f, err := st.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return b, nil
}

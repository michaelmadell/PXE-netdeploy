// Package bootimage builds boot images (ADR 0005, 0006, 0022): WinPE from a
// Windows image's boot.wim with the agent, its configuration and drivers
// added, plus the files Boot Manager loads, stored as a content-store tree.
package bootimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agentbin"
	"github.com/michaelmadell/pxe-netdeploy/internal/bcd"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/signer"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/wim"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// KindBuild is the build job kind.
const KindBuild = "bootImage.build"

// Defaults (BootImageInput).
const (
	DefaultSourceIndex    = 1
	DefaultTFTPBlockSize  = 16384
	DefaultTFTPWindowSize = 16
)

// BCDOptions are what a boot image's BCD depends on.
type BCDOptions struct {
	Architecture   string // x64 or arm64
	TFTPBlockSize  int
	TFTPWindowSize int
}

// BCDWriter writes a BCD store for a boot image's tree. work is a scratch
// directory.
type BCDWriter interface {
	WriteBCD(ctx context.Context, work string, o BCDOptions) ([]byte, error)
}

// builtinBCD writes the RAM-disk store for the tree layout (ADR 0023).
type builtinBCD struct{}

func (builtinBCD) WriteBCD(_ context.Context, _ string, o BCDOptions) ([]byte, error) {
	tftp := func(p string) string { return `\` + strings.ReplaceAll(p, "/", `\`) }
	return bcd.RAMDisk{
		WIMPath: tftp(TreeWIM), SDIPath: tftp(TreeSDI),
		TFTPBlockSize: o.TFTPBlockSize, TFTPWindowSize: o.TFTPWindowSize,
		Description: "netdeploy WinPE", TimeoutSeconds: 5,
	}.Bytes()
}

// Options configure a Service.
type Options struct {
	Store   *store.Store
	Content *cas.Store
	Agents  *agentbin.Set
	Events  *events.Bus
	Logger  *slog.Logger
	// WimlibPath overrides the PATH lookup of wimlib-imagex (-wimlib).
	WimlibPath string
	// BCD writes BCD stores. Nil: the built-in writer (internal/bcd).
	BCD BCDWriter
	// Detect and NewTool replace host probing and the real tools in tests.
	Detect  func(ctx context.Context) Host
	NewTool func(name string, h Host) Tool
	// Classify returns a boot manager's signer (signer.Signer values). Nil:
	// signer.Default().
	Classify func(b []byte) string
}

// Service defines, checks and builds boot images.
type Service struct {
	st       *store.Store
	content  *cas.Store
	agents   *agentbin.Set
	events   *events.Bus
	log      *slog.Logger
	bcd      BCDWriter
	detect   func(ctx context.Context) Host
	newTool  func(name string, h Host) Tool
	classify func(b []byte) string

	hostMu sync.Mutex
	hostAt time.Time
	host   Host
}

// hostTTL bounds how stale the probed builders can be.
const hostTTL = time.Minute

// New returns a Service.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Detect == nil {
		path := o.WimlibPath
		o.Detect = func(ctx context.Context) Host { return Detect(ctx, path) }
	}
	if o.NewTool == nil {
		o.NewTool = newTool
	}
	if o.BCD == nil {
		o.BCD = builtinBCD{}
	}
	if o.Classify == nil {
		o.Classify = func(b []byte) string {
			res, err := signer.Default().Classify(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				return string(signer.Unknown)
			}
			return string(res.Signer)
		}
	}
	return &Service{st: o.Store, content: o.Content, agents: o.Agents, events: o.Events, log: o.Logger.With("svc", "boot-images"),
		bcd: o.BCD, detect: o.Detect, newTool: o.NewTool, classify: o.Classify}
}

// Kind is the job kind to register with the runner.
func (s *Service) Kind() jobs.Kind {
	return jobs.Kind{Name: KindBuild, Lane: jobs.LaneBuild, Run: s.run, Abandon: s.abandon}
}

// Host returns the probed builders (cached for a minute).
func (s *Service) Host(ctx context.Context) Host {
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	if s.hostAt.IsZero() || time.Since(s.hostAt) > hostTTL {
		s.host = s.detect(ctx)
		s.hostAt = time.Now()
	}
	return s.host
}

// Agents returns the bundled agents.
func (s *Service) Agents() *agentbin.Set { return s.agents }

func (s *Service) publish(id string) {
	if s.events != nil {
		s.events.Publish(events.Event{Type: events.BootImageUpdated, ResourceID: id})
	}
}

// Publish sends bootImage.updated for ids (e.g. after their drivers
// changed, which can make them stale).
func (s *Service) Publish(ids ...string) {
	for _, id := range ids {
		s.publish(id)
	}
}

func unprocessable(code, title, detail string) *jobs.Error {
	return jobs.Fail(http.StatusUnprocessableEntity, code, title, detail)
}

// ---------------------------------------------------------------- checks

// windowsDetails is the part of WindowsImageDetails a boot image needs.
type windowsDetails struct {
	Architecture     string `json:"architecture"`
	BootImageIndexes []struct {
		Index int    `json:"index"`
		Name  string `json:"name"`
	} `json:"bootImageIndexes"`
}

// sourceImage loads a ready windows image and its details.
func (s *Service) sourceImage(ctx context.Context, id string) (store.Image, windowsDetails, *jobs.Error) {
	im, err := s.st.Image(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return im, windowsDetails{}, unprocessable("source_image_unavailable", "Source image unavailable", "No image "+id+".")
	}
	if err != nil {
		return im, windowsDetails{}, jobs.AsError(err)
	}
	if im.Status != store.ImageReady || im.Kind == nil || *im.Kind != "windows" || im.TreeID == nil {
		return im, windowsDetails{}, unprocessable("source_image_unavailable", "Source image unavailable",
			fmt.Sprintf("Image %q is not a ready Windows image.", im.Name))
	}
	var d windowsDetails
	if err := json.Unmarshal(im.Details, &d); err != nil {
		return im, d, jobs.AsError(fmt.Errorf("image %s details: %w", id, err))
	}
	return im, d, nil
}

// Check validates inputs for create and replace: everything that doesn't
// depend on the host or on driver contents. Driver groups are checked by
// the store (ErrUnknownDriverGroup).
func (s *Service) Check(ctx context.Context, in store.BootImageInput) *jobs.Error {
	if in.Name == "" || len(in.Name) > 200 {
		return jobs.Fail(http.StatusBadRequest, "invalid_request", "Invalid request", "name must be 1 to 200 characters.")
	}
	if len(in.ServerURLs) == 0 {
		return jobs.Fail(http.StatusBadRequest, "invalid_request", "Invalid request", "serverUrls needs at least one URL.")
	}
	for _, u := range in.ServerURLs {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return jobs.Fail(http.StatusBadRequest, "invalid_request", "Invalid request", fmt.Sprintf("serverUrls: %q is not an http(s) URL.", u))
		}
	}
	if len(in.OptionalComponents) > 0 {
		return unprocessable("not_implemented", "Not implemented", "Optional components aren't supported yet (ADR 0022).")
	}
	if in.ProduceHTTPBootISO {
		return unprocessable("not_implemented", "Not implemented", "HTTP Boot ISOs aren't supported yet (Phase 1 task 9).")
	}
	if in.AgentVersion != nil && (s.agents == nil || *in.AgentVersion != s.agents.Version) {
		return unprocessable("agent_unavailable", "Agent unavailable", fmt.Sprintf("Only the bundled agent (%s) can be used.", s.agentVersion()))
	}
	im, d, perr := s.sourceImage(ctx, in.SourceImageID)
	if perr != nil {
		return perr
	}
	if d.Architecture != in.Architecture {
		return unprocessable("architecture_mismatch", "Architecture mismatch",
			fmt.Sprintf("Image %q is %s, not %s.", im.Name, d.Architecture, in.Architecture))
	}
	found := false
	var indexes []string
	for _, x := range d.BootImageIndexes {
		indexes = append(indexes, fmt.Sprint(x.Index))
		found = found || x.Index == in.SourceIndex
	}
	if !found {
		return unprocessable("unknown_source_index", "Unknown boot.wim index",
			fmt.Sprintf("boot.wim of %q has indexes %s, not %d.", im.Name, strings.Join(indexes, ", "), in.SourceIndex))
	}
	bms, err := s.st.ImageBootManagers(ctx, im.ID)
	if err != nil {
		return jobs.AsError(err)
	}
	prefix := fmt.Sprintf("boot.wim:%d/", in.SourceIndex)
	var signers []string
	for _, b := range bms {
		if strings.HasPrefix(b.SourcePath, prefix) {
			signers = append(signers, b.Signer)
		}
	}
	want := []string{string(signer.WindowsUEFICA2023), string(signer.WindowsPCA2011)}
	if in.Bootmgr != "auto" {
		want = []string{in.Bootmgr}
	}
	if !slices.ContainsFunc(want, func(w string) bool { return slices.Contains(signers, w) }) {
		return unprocessable("bootmgr_unavailable", "Boot manager unavailable",
			fmt.Sprintf("boot.wim index %d of %q has no boot manager signed %s.", in.SourceIndex, im.Name, strings.Join(want, " or ")))
	}
	return nil
}

func (s *Service) agentVersion() string {
	if s.agents == nil {
		return "none"
	}
	return s.agents.Version
}

// ---------------------------------------------------------------- plan

// plan is a boot image's resolved inputs.
type plan struct {
	inputs   Inputs
	hash     string
	image    store.Image
	bootWIM  string // blob path
	wimSize  int64
	build    string // boot.wim index build, e.g. 26100.9457
	drivers  []store.BootDriver
	agent    agentbin.Binary
	tool     Tool
	warnings []string
}

// plan resolves bi's inputs. For a status check (forBuild false) it
// tolerates what only matters to a build (no builder, no agent, driver
// problems), leaving those inputs unknown.
func (s *Service) plan(ctx context.Context, bi store.BootImage, forBuild bool) (plan, *jobs.Error) {
	p := plan{inputs: Inputs{
		Recipe: Recipe, SourceIndex: bi.SourceIndex, Architecture: bi.Architecture, ServerURLs: bi.ServerURLs,
		Bootmgr: bi.Bootmgr, TFTPBlockSize: bi.TFTPBlockSize, TFTPWindowSize: bi.TFTPWindowSize,
	}}
	im, _, perr := s.sourceImage(ctx, bi.SourceImageID)
	if perr != nil {
		return p, perr
	}
	p.image = im
	e, err := s.st.LookupTreeEntry(ctx, *im.TreeID, cas.Fold("sources/boot.wim"))
	if err != nil {
		return p, unprocessable("source_image_unavailable", "Source image unavailable", fmt.Sprintf("Image %q has no sources/boot.wim.", im.Name))
	}
	p.inputs.BootWIM, p.wimSize = e.SHA256, e.Size
	if p.bootWIM, err = s.content.BlobPath(e.SHA256); err != nil {
		return p, jobs.AsError(err)
	}

	if a, err := s.agents.Get(bi.Architecture); err == nil {
		p.agent, p.inputs.Agent = a, a.SHA256
	} else if forBuild {
		return p, unprocessable("agent_unavailable", "Agent unavailable",
			fmt.Sprintf("This server has no bundled agent for %s (build with `just build`, or pass -agent-dir).", bi.Architecture))
	}

	if p.drivers, err = s.st.BootDrivers(ctx, bi.DriverGroupIDs); err != nil {
		return p, jobs.AsError(err)
	}
	for _, d := range p.drivers {
		p.inputs.Drivers = append(p.inputs.Drivers, DriverInput{Package: d.PackageSHA256, Path: d.Path})
		if !forBuild {
			continue
		}
		switch {
		case d.Signature == "unsigned":
			return p, unprocessable("unsigned_driver", "Unsigned driver",
				fmt.Sprintf("%s (package %s) is unsigned; Secure Boot WinPE won't load it. Disable it or remove the package from the group.", d.Path, d.PackageID))
		case !slices.Contains(d.Architectures, bi.Architecture):
			return p, unprocessable("driver_architecture", "Driver doesn't fit the architecture",
				fmt.Sprintf("%s (package %s) doesn't support %s.", d.Path, d.PackageID, bi.Architecture))
		case d.Signature == "vendor":
			p.warnings = append(p.warnings, fmt.Sprintf("%s is signed by its vendor, not Microsoft: it loads only if the vendor's certificate is trusted.", d.Path))
		case d.Signature == "unknown":
			p.warnings = append(p.warnings, fmt.Sprintf("%s: its signature couldn't be checked.", d.Path))
		}
	}

	// The builder.
	if bi.Builder != "auto" {
		p.inputs.Builder = bi.Builder
	}
	if !forBuild {
		p.hash = p.inputs.Hash()
		return p, nil
	}
	h := s.Host(ctx)
	if p.build, err = indexBuild(p.bootWIM, bi.SourceIndex); err != nil {
		return p, jobs.AsError(err)
	}
	dismOK, dismWhy := h.DISM.Available, h.DISM.Reason
	if dismOK && dismBuild(h.DISM.Version) < imageBuild(p.build) {
		dismOK, dismWhy = false, fmt.Sprintf("DISM %s is older than the image (build %s).", h.DISM.Version, p.build)
	}
	switch {
	case bi.Builder == BuilderDISM && !dismOK:
		return p, unprocessable("builder_unavailable", "Builder unavailable", dismWhy)
	case bi.Builder == BuilderWimlib && !h.Wimlib.Available:
		return p, unprocessable("builder_unavailable", "Builder unavailable", h.Wimlib.Reason)
	// auto prefers wimlib: about 35 times faster, unprivileged, and since
	// WinPE runs from a RAM disk the agent can load any driver, storage
	// included, with drvload (ADR 0022).
	case bi.Builder == "auto" && h.Wimlib.Available:
		p.inputs.Builder = BuilderWimlib
	case bi.Builder == "auto" && dismOK:
		p.inputs.Builder = BuilderDISM
	case bi.Builder == "auto":
		return p, unprocessable("builder_unavailable", "No builder available", h.Wimlib.Reason+" "+dismWhy)
	}
	p.tool = s.newTool(p.inputs.Builder, h)
	p.hash = p.inputs.Hash()
	return p, nil
}

// indexBuild reads the build number of one boot.wim image from its XML.
func indexBuild(path string, index int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := wim.ReadInfo(f)
	if err != nil {
		return "", fmt.Errorf("source boot.wim: %w", err)
	}
	for _, im := range info.Images {
		if im.Index == index {
			return im.Build, nil
		}
	}
	return "", unprocessable("unknown_source_index", "Unknown boot.wim index", fmt.Sprintf("boot.wim has no index %d.", index))
}

// ---------------------------------------------------------------- status

// Status is a boot image's API status and stale reasons.
func (s *Service) Status(ctx context.Context, bi store.BootImage) (string, []string) {
	switch bi.State {
	case store.BootImageDraft:
		return "draft", nil
	case store.BootImageBuilding:
		return "building", nil
	case store.BootImageFailed:
		return "failed", nil
	}
	var built Inputs
	if err := json.Unmarshal(bi.BuiltInputs, &built); err != nil {
		return "ready", nil
	}
	p, perr := s.plan(ctx, bi, false)
	if perr != nil {
		return "ready", nil
	}
	if reasons := Diff(built, p.inputs); len(reasons) > 0 {
		return "stale", reasons
	}
	return "ready", nil
}

// ---------------------------------------------------------------- build

// Build starts a build of boot image id: it returns the boot image and a
// queued job, or (unless force) the boot image without a job when a build
// with the same inputs is already served (ADR 0022). Errors are
// *jobs.Error (4xx/507), store.ErrNotFound or store.ErrBuildInProgress.
func (s *Service) Build(ctx context.Context, id string, force bool) (store.BootImage, *store.Job, error) {
	bi, err := s.st.BootImage(ctx, id)
	if err != nil {
		return bi, nil, err
	}
	if bi.State == store.BootImageBuilding {
		return bi, nil, store.ErrBuildInProgress
	}
	p, perr := s.plan(ctx, bi, true)
	if perr != nil {
		return bi, nil, perr
	}
	if !force {
		if bi.InputHash != nil && *bi.InputHash == p.hash && bi.TreeID != nil {
			if bi.State == store.BootImageFailed {
				if err := s.adopt(ctx, id, bi); err != nil {
					return bi, nil, err
				}
				bi, err = s.st.BootImage(ctx, id)
			}
			return bi, nil, err
		}
		if other, err := s.st.BootImageWithBuild(ctx, p.hash, id); err == nil {
			if err := s.adopt(ctx, id, other); err != nil {
				return bi, nil, err
			}
			bi, err = s.st.BootImage(ctx, id)
			return bi, nil, err
		} else if !errors.Is(err, store.ErrNotFound) {
			return bi, nil, err
		}
	}
	if err := s.content.CheckSpace(4 * p.wimSize); err != nil {
		return bi, nil, err
	}
	j, err := s.st.QueueBootImageBuild(ctx, id, KindBuild, nil)
	if err != nil {
		return bi, nil, err
	}
	s.publish(id)
	bi, err = s.st.BootImage(ctx, id)
	return bi, &j, err
}

// adopt points id at from's build.
func (s *Service) adopt(ctx context.Context, id string, from store.BootImage) error {
	err := s.st.AdoptBootImageBuild(ctx, id, store.BootBuild{
		TreeID: *from.TreeID, InputHash: *from.InputHash, Inputs: from.BuiltInputs, BuiltWith: *from.BuiltWith,
		SelectedBootManager: from.SelectedBootManager, Warnings: from.Warnings,
	})
	if err == nil {
		s.publish(id)
	}
	return err
}

func (s *Service) run(ctx context.Context, r *jobs.Run) error {
	if r.Job.ResourceID == nil {
		return errors.New("bad job: no boot image")
	}
	id := *r.Job.ResourceID
	bi, err := s.st.BootImage(ctx, id)
	if err != nil {
		return err
	}
	p, perr := s.plan(ctx, bi, true)
	if perr != nil {
		return s.fail(context.WithoutCancel(ctx), id, perr)
	}
	err = s.build(ctx, r, bi, p)
	if err == nil {
		s.publish(id)
		return nil
	}
	if ctx.Err() != nil && !r.CancelRequested() {
		return err // shutting down: Abandon records it
	}
	if ctx.Err() != nil {
		err = jobs.Fail(http.StatusConflict, "cancelled", "Build cancelled", "The build was cancelled.")
	}
	return s.fail(context.WithoutCancel(ctx), id, err)
}

func (s *Service) fail(ctx context.Context, id string, cause error) error {
	if err := s.st.FailBootImageBuild(ctx, id, jobs.Problem(cause)); err != nil {
		s.log.Error("record boot image failure", "boot_image_id", id, "err", err)
	}
	s.publish(id)
	return cause
}

func (s *Service) abandon(ctx context.Context, j store.Job, reason string) error {
	if j.ResourceID == nil {
		return nil
	}
	e := jobs.Fail(http.StatusServiceUnavailable, reason, "Build "+reason, "")
	switch reason {
	case "interrupted":
		e.Detail = "The server stopped during the build. Build it again."
	case "cancelled":
		e.Status, e.Detail = http.StatusConflict, "The build was cancelled before it started."
	case "failed":
		e.Code, e.Status, e.Detail = "internal_error", http.StatusInternalServerError, "The build failed unexpectedly."
	}
	s.fail(ctx, *j.ResourceID, e)
	return nil
}

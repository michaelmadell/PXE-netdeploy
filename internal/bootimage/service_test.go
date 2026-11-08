package bootimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	gowim "github.com/nradchenko/go-wim"

	"github.com/michaelmadell/pxe-netdeploy/internal/agentbin"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// A minimal self-relative security descriptor (no owner, no DACL).
var sd = []byte{1, 0, 0x04, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

// peFiles is a stand-in WinPE image with the boot files a build reads.
var peFiles = fstest.MapFS{
	wimBootmgr2011:                      {Data: []byte("PCA2011 boot manager")},
	wimBootmgr2023:                      {Data: []byte("UEFICA2023 boot manager")},
	wimSDI:                              {Data: []byte("sdi")},
	wimFonts + "/wgl4_boot.ttf":         {Data: []byte("font")},
	wimFonts + "/segmono_boot.ttf":      {Data: []byte("mono")},
	wimFontsEX + "/wgl4_boot_EX.ttf":    {Data: []byte("font EX")},
	wimFontsEX + "/segmono_boot_EX.ttf": {Data: []byte("mono EX")},
	"Windows/System32/drvload.exe":      {Data: []byte("MZ")},
}

func makeWIM(t *testing.T, files fs.FS) []byte {
	t.Helper()
	var out writeSeeker
	w := gowim.NewWriter(&out, gowim.Options{Security: gowim.UniformSecurity(sd), Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	info := gowim.ImageInfo{Name: "Microsoft Windows PE (amd64)", Boot: true,
		Windows: &gowim.WindowsInfo{Arch: 9, Version: gowim.Version{Major: 10, Build: 26100, SPBuild: 9457}}}
	if err := w.AddImage(context.Background(), files, info); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.b
}

type writeSeeker struct {
	b   []byte
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	if end := w.pos + int64(len(p)); end > int64(len(w.b)) {
		w.b = append(w.b, make([]byte, end-int64(len(w.b)))...)
	}
	copy(w.b[w.pos:], p)
	w.pos += int64(len(p))
	return len(p), nil
}

func (w *writeSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = off
	case io.SeekCurrent:
		w.pos += off
	case io.SeekEnd:
		w.pos = int64(len(w.b)) + off
	}
	return w.pos, nil
}

// fakeTool records what a build asked for and passes the WIM through.
type fakeTool struct {
	name string
	mu   sync.Mutex
	fail error
	seen []map[string]string // per Customize: added files → contents
	infs [][]string
}

func (f *fakeTool) Name() string { return f.name }

func (f *fakeTool) Export(_ context.Context, src string, index int, dst string) error {
	if index != 1 {
		return fmt.Errorf("index %d", index)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

func (f *fakeTool) Customize(_ context.Context, c Customization) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	added := map[string]string{}
	filepath.WalkDir(c.Files, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(c.Files, p)
			b, _ := os.ReadFile(p)
			added[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	f.seen = append(f.seen, added)
	var infs []string
	for _, inf := range c.Drivers {
		b, err := os.ReadFile(inf)
		if err != nil {
			return err
		}
		infs = append(infs, filepath.Base(inf)+"="+string(b))
	}
	f.infs = append(f.infs, infs)
	return os.Rename(c.WIM, c.Out)
}

type fakeBCD struct{}

func (fakeBCD) WriteBCD(_ context.Context, _ string, o BCDOptions) ([]byte, error) {
	return []byte(fmt.Sprintf("BCD %s %d/%d", o.Architecture, o.TFTPBlockSize, o.TFTPWindowSize)), nil
}

func classify(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("UEFICA2023")):
		return "windows-uefi-ca-2023"
	case bytes.HasPrefix(b, []byte("PCA2011")):
		return "windows-pca-2011"
	}
	return "unsigned"
}

type fixture struct {
	ctx     context.Context
	st      *store.Store
	content *cas.Store
	svc     *Service
	tools   map[string]*fakeTool
	host    Host
	image   store.Image
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	content, err := cas.Open(ctx, cas.Options{Root: filepath.Join(dir, "content"), Store: st, Reserve: func() int64 { return 0 }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, st: st, content: content,
		tools: map[string]*fakeTool{BuilderDISM: {name: BuilderDISM}, BuilderWimlib: {name: BuilderWimlib}},
		host: Host{DISM: ToolStatus{Name: BuilderDISM, Reason: "DISM needs elevation."},
			Wimlib: ToolStatus{Name: BuilderWimlib, Available: true, Version: "1.14.4", Path: "wimlib-imagex"}}}
	f.svc = New(Options{Store: st, Content: content, Logger: quiet, BCD: fakeBCD{}, Classify: classify,
		Agents:  agentbin.ForTest("v1", map[string][]byte{"x64": []byte("MZ agent")}),
		Detect:  func(context.Context) Host { return f.host },
		NewTool: func(name string, _ Host) Tool { return f.tools[name] }})

	// A ready Windows image whose tree holds sources/boot.wim.
	up := f.upload(t, strings.Repeat("a", 64))
	im, _, err := st.CreateImageImport(ctx, store.NewImageImport{Name: "Win11", UploadID: up}, "image.import")
	if err != nil {
		t.Fatal(err)
	}
	stg, _ := content.NewStaging("image")
	wimSHA, _, err := stg.Add("sources/boot.wim", bytes.NewReader(makeWIM(t, peFiles)))
	if err != nil {
		t.Fatal(err)
	}
	x64 := "x64"
	if _, err := stg.Commit(ctx, store.TreeImage, func(ctx context.Context, tree string) error {
		return st.CompleteImageImport(ctx, im.ID, store.ImportResult{Kind: "windows", TreeID: tree,
			Details:      json.RawMessage(`{"kind":"windows","architecture":"x64","bootImageIndexes":[{"index":1,"name":"PE"}]}`),
			BootManagers: []store.BootManager{{SourcePath: "boot.wim:1/" + wimBootmgr2023, SHA256: wimSHA, Signer: "windows-uefi-ca-2023", Architecture: &x64}}})
	}); err != nil {
		t.Fatal(err)
	}
	f.image, _ = st.Image(ctx, im.ID)
	return f
}

func (f *fixture) upload(t *testing.T, sha string) string {
	t.Helper()
	name := "x"
	u, err := f.st.CreateUpload(f.ctx, store.NewUpload{Length: 10, Filename: &name, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.CheckpointUpload(f.ctx, u.ID, store.UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// driverGroup imports a one-INF package and puts it in a new group.
func (f *fixture) driverGroup(t *testing.T, sha byte, role, signature string) string {
	t.Helper()
	pkg, _, err := f.st.CreateDriverImport(f.ctx, "drv", f.upload(t, strings.Repeat(string(sha), 64)), "driverPackage.import")
	if err != nil {
		t.Fatal(err)
	}
	stg, _ := f.content.NewStaging("drv" + string(sha))
	stg.Add("Net/e1r.inf", strings.NewReader("[Version] "+string(sha)))
	stg.Add("Net/e1r.sys", strings.NewReader("sys"))
	stg.Add("Other/x.inf", strings.NewReader("other"))
	if _, err := stg.Commit(f.ctx, store.TreeDriverPackage, func(ctx context.Context, tree string) error {
		return f.st.CompleteDriverImport(ctx, pkg.ID, tree, nil, []store.NewDriverINF{{
			Path: "Net/e1r.inf", Class: "Net", Role: role, Architectures: []string{"x64"}, TargetOSVersions: []string{},
			Signature: signature, Enabled: true,
		}})
	}); err != nil {
		t.Fatal(err)
	}
	g, err := f.st.CreateDriverGroup(f.ctx, store.DriverGroupInput{Name: "g", PackageIDs: []string{pkg.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return g.ID
}

func (f *fixture) input(name string) store.BootImageInput {
	return store.BootImageInput{Name: name, SourceImageID: f.image.ID, SourceIndex: 1, Architecture: "x64", DriverGroupIDs: []string{},
		ServerURLs: []string{"http://10.77.0.1:8080"}, Builder: "auto", Bootmgr: "auto", TFTPBlockSize: 16384, TFTPWindowSize: 16}
}

func (f *fixture) runner(t *testing.T) {
	t.Helper()
	r := jobs.New(jobs.Options{Store: f.st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PollInterval: 50 * time.Millisecond})
	r.Register(f.svc.Kind())
	ctx, cancel := context.WithCancel(f.ctx)
	t.Cleanup(func() { cancel(); <-r.Done() })
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
}

// build starts a build and waits for its job, if any.
func (f *fixture) build(t *testing.T, id string, force bool) (store.BootImage, *store.Job) {
	t.Helper()
	bi, j, err := f.svc.Build(f.ctx, id, force)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if j == nil {
		return bi, nil
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := f.st.Job(f.ctx, j.ID)
		if got.Finished() {
			bi, _ = f.st.BootImage(f.ctx, id)
			return bi, &got
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s", j.ID, got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *fixture) treeFiles(t *testing.T, treeID string) map[string]string {
	t.Helper()
	es, err := f.st.TreeEntries(f.ctx, treeID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range es {
		if e.Path == TreeWIM {
			out[e.Path] = "(wim)"
			continue
		}
		b, _ := fs.ReadFile(f.content.Tree(treeID), e.Path)
		out[e.Path] = string(b)
	}
	return out
}

func code(err error) string {
	var je *jobs.Error
	if errors.As(err, &je) {
		return je.Code
	}
	return fmt.Sprint(err)
}

func TestCheck(t *testing.T) {
	f := newFixture(t)
	ok := f.input("pe")
	if e := f.svc.Check(f.ctx, ok); e != nil {
		t.Fatalf("valid input: %v", e)
	}
	other := "v0"
	for name, c := range map[string]struct {
		mod  func(*store.BootImageInput)
		want string
	}{
		"arch":      {func(in *store.BootImageInput) { in.Architecture = "arm64" }, "architecture_mismatch"},
		"index":     {func(in *store.BootImageInput) { in.SourceIndex = 2 }, "unknown_source_index"},
		"bootmgr":   {func(in *store.BootImageInput) { in.Bootmgr = "windows-pca-2011" }, "bootmgr_unavailable"},
		"ocs":       {func(in *store.BootImageInput) { in.OptionalComponents = []string{"WinPE-WMI"} }, "not_implemented"},
		"iso":       {func(in *store.BootImageInput) { in.ProduceHTTPBootISO = true }, "not_implemented"},
		"agent":     {func(in *store.BootImageInput) { in.AgentVersion = &other }, "agent_unavailable"},
		"image":     {func(in *store.BootImageInput) { in.SourceImageID = "nope" }, "source_image_unavailable"},
		"url":       {func(in *store.BootImageInput) { in.ServerURLs = []string{"ftp://x"} }, "invalid_request"},
		"no urls":   {func(in *store.BootImageInput) { in.ServerURLs = nil }, "invalid_request"},
		"long name": {func(in *store.BootImageInput) { in.Name = strings.Repeat("n", 201) }, "invalid_request"},
	} {
		in := f.input("pe")
		c.mod(&in)
		if e := f.svc.Check(f.ctx, in); e == nil || e.Code != c.want {
			t.Errorf("%s: got %v, want %s", name, e, c.want)
		}
	}
}

func TestBuildLifecycle(t *testing.T) {
	f := newFixture(t)
	f.runner(t)
	bi, err := f.st.CreateBootImage(f.ctx, f.input("pe"))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := f.svc.Status(f.ctx, bi); s != "draft" {
		t.Fatalf("status %s", s)
	}

	// First build: wimlib (DISM isn't elevated), the 2023 boot manager and
	// its fonts under the plain names.
	bi, j := f.build(t, bi.ID, false)
	if j == nil || j.Status != store.JobSucceeded {
		t.Fatalf("job: %+v", j)
	}
	if s, r := f.svc.Status(f.ctx, bi); s != "ready" || r != nil || *bi.BuiltWith != BuilderWimlib {
		t.Fatalf("status %s %v, built with %v", s, r, bi.BuiltWith)
	}
	files := f.treeFiles(t, *bi.TreeID)
	want := map[string]string{
		TreeBootmgr: "UEFICA2023 boot manager", TreeBCD: "BCD x64 16384/16", TreeSDI: "sdi", TreeWIM: "(wim)",
		TreeFonts + "/wgl4_boot.ttf": "font EX", TreeFonts + "/segmono_boot.ttf": "mono EX",
	}
	if fmt.Sprint(files) != fmt.Sprint(want) {
		t.Fatalf("tree:\n got %v\nwant %v", files, want)
	}
	var sbm map[string]string
	json.Unmarshal(bi.SelectedBootManager, &sbm)
	if sbm["signer"] != "windows-uefi-ca-2023" || sbm["sourcePath"] != "boot.wim:1/"+wimBootmgr2023 {
		t.Errorf("selected boot manager: %v", sbm)
	}
	added := f.tools[BuilderWimlib].seen[0]
	if added["netdeploy/agent.exe"] != "MZ agent" || added[winpeshlPath] != winpeshl {
		t.Errorf("added files: %v", added)
	}
	var cfg AgentConfig
	json.Unmarshal([]byte(added["netdeploy/config.json"]), &cfg)
	if cfg.InputHash != *bi.InputHash || len(cfg.Drivers) != 0 || cfg.ServerURLs[0] != "http://10.77.0.1:8080" {
		t.Errorf("config: %+v", cfg)
	}

	// Unchanged: no job. Same inputs on another boot image: reuses the tree.
	if again, j := f.build(t, bi.ID, false); j != nil || again.Version != bi.Version {
		t.Fatalf("rebuild of unchanged inputs started a job: %+v", j)
	}
	twin, _ := f.st.CreateBootImage(f.ctx, f.input("twin"))
	twin, j = f.build(t, twin.ID, false)
	if j != nil || twin.TreeID == nil || *twin.TreeID != *bi.TreeID {
		t.Fatalf("twin: job %+v, tree %v", j, twin.TreeID)
	}
	if len(f.tools[BuilderWimlib].seen) != 1 {
		t.Fatal("twin was built")
	}

	// Inputs change: stale, still serving the old build.
	in := f.input("pe")
	in.ServerURLs = []string{"http://10.77.0.2:8080"}
	bi, err = f.st.ReplaceBootImage(f.ctx, bi.ID, in, bi.Version)
	if err != nil {
		t.Fatal(err)
	}
	if s, r := f.svc.Status(f.ctx, bi); s != "stale" || !slices.Equal(r, []string{ReasonServerURLs}) || bi.TreeID == nil {
		t.Fatalf("status %s %v", s, r)
	}

	// A network driver: staged for drvload under netdeploy\drivers.
	g := f.driverGroup(t, 'b', "network", "whql")
	in.DriverGroupIDs = []string{g}
	bi, _ = f.st.ReplaceBootImage(f.ctx, bi.ID, in, bi.Version)
	if _, r := f.svc.Status(f.ctx, bi); !slices.Equal(r, []string{ReasonServerURLs, ReasonDrivers}) {
		t.Fatalf("reasons %v", r)
	}
	oldTree := *bi.TreeID
	bi, j = f.build(t, bi.ID, false)
	if j.Status != store.JobSucceeded || *bi.TreeID == oldTree {
		t.Fatalf("driver build: %+v", j)
	}
	added = f.tools[BuilderWimlib].seen[1]
	json.Unmarshal([]byte(added["netdeploy/config.json"]), &cfg)
	if !slices.Equal(cfg.Drivers, []string{`drivers\1\e1r.inf`}) || added["netdeploy/drivers/1/e1r.inf"] != "[Version] b" ||
		added["netdeploy/drivers/1/e1r.sys"] != "sys" || added["netdeploy/drivers/1/x.inf"] != "" {
		t.Fatalf("staged drivers: %v / %v", cfg.Drivers, added)
	}

	// DISM available too: auto still prefers wimlib (ADR 0022), so nothing
	// changed and there's no job.
	f.host.DISM = ToolStatus{Name: BuilderDISM, Available: true, Version: "10.0.26100.9549", Path: "dism.exe"}
	f.svc.hostAt = time.Time{}
	if _, j := f.build(t, bi.ID, false); j != nil {
		t.Fatalf("auto switched builder: %+v", j)
	}
	// Without wimlib: DISM, with the INF injected and nothing staged.
	f.host.Wimlib.Available = false
	f.svc.hostAt = time.Time{}
	bi, j = f.build(t, bi.ID, true)
	if j == nil || j.Status != store.JobSucceeded || *bi.BuiltWith != BuilderDISM {
		t.Fatalf("dism build: %+v %v", j, bi.BuiltWith)
	}
	dt := f.tools[BuilderDISM]
	if !slices.Equal(dt.infs[0], []string{"e1r.inf=[Version] b"}) || dt.seen[0]["netdeploy/drivers/1/e1r.inf"] != "" {
		t.Fatalf("dism drivers: %v", dt.infs)
	}
	// A DISM older than the image and no wimlib: no builder.
	f.host.DISM.Version = "10.0.22621.1"
	f.svc.hostAt = time.Time{}
	if _, _, err := f.svc.Build(f.ctx, bi.ID, true); code(err) != "builder_unavailable" {
		t.Fatalf("old DISM: %v", err)
	}
	f.host.Wimlib.Available = true
	f.svc.hostAt = time.Time{}
	if bi, j = f.build(t, bi.ID, true); j == nil || j.Status != store.JobSucceeded || *bi.BuiltWith != BuilderWimlib {
		t.Fatalf("wimlib again: %+v", j)
	}

	// A failed rebuild keeps serving the last build.
	served := *bi.TreeID
	f.tools[BuilderWimlib].fail = errors.New("boom")
	bi, j = f.build(t, bi.ID, true)
	f.tools[BuilderWimlib].fail = nil
	if j.Status != store.JobFailed || bi.TreeID == nil || *bi.TreeID != served {
		t.Fatalf("failed build: %+v, tree %v", j, bi.TreeID)
	}
	if s, _ := f.svc.Status(f.ctx, bi); s != "failed" {
		t.Fatalf("status %s", s)
	}
	if !strings.Contains(string(bi.Error), `"build_failed"`) || !strings.Contains(string(bi.Error), "boom") {
		t.Errorf("error %s", bi.Error)
	}
	// Building the same inputs again re-adopts the served build without a job.
	if bi, j = f.build(t, bi.ID, false); j != nil || bi.State != store.BootImageBuilt {
		t.Fatalf("after failure: %+v %s", j, bi.State)
	}

	// In use: the image and the group can't be deleted.
	if err := f.st.DeleteImage(f.ctx, f.image.ID); !errors.Is(err, store.ErrImageInUse) {
		t.Errorf("delete image: %v", err)
	}
	if err := f.st.DeleteDriverGroup(f.ctx, g); !errors.Is(err, store.ErrDriverGroupInUse) {
		t.Errorf("delete group: %v", err)
	}
	// Deleting both boot images frees their builds.
	for _, id := range []string{bi.ID, twin.ID} {
		if err := f.st.DeleteBootImage(f.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.content.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.st.TreeEntries(f.ctx, served); len(es) > 0 {
		t.Error("build tree not swept")
	}
}

func TestBuildRefusals(t *testing.T) {
	f := newFixture(t)
	mk := func(name string, groups ...string) string {
		in := f.input(name)
		in.DriverGroupIDs = groups
		bi, err := f.st.CreateBootImage(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return bi.ID
	}
	plain := mk("plain")
	unsigned := mk("unsigned", f.driverGroup(t, 'c', "network", "unsigned"))
	storage := mk("storage", f.driverGroup(t, 'd', "storage", "whql"))

	for name, c := range map[string]struct {
		id   string
		prep func()
		want string
	}{
		"unsigned driver": {unsigned, nil, "unsigned_driver"},
		"no builder":      {plain, func() { f.host.Wimlib.Available = false }, "builder_unavailable"},
		"no agent":        {plain, func() { f.svc.agents = agentbin.ForTest("v1", nil) }, "agent_unavailable"},
	} {
		saved, savedHost, savedAgents := f.svc.bcd, f.host, f.svc.agents
		if c.prep != nil {
			c.prep()
		}
		f.svc.hostAt = time.Time{}
		_, j, err := f.svc.Build(f.ctx, c.id, false)
		if j != nil || code(err) != c.want {
			t.Errorf("%s: job %v, err %v, want %s", name, j, err, c.want)
		}
		f.svc.bcd, f.host, f.svc.agents = saved, savedHost, savedAgents
		f.svc.hostAt = time.Time{}
	}

	// Storage drivers build with wimlib too: the agent loads them with
	// drvload (WinPE runs from a RAM disk, ADR 0022).
	if _, j, err := f.svc.Build(f.ctx, storage, false); err != nil || j == nil {
		t.Errorf("storage driver with wimlib: %v", err)
	}

	// A second request while queued.
	if _, j, err := f.svc.Build(f.ctx, plain, false); err != nil || j == nil {
		t.Fatalf("queue: %v", err)
	}
	if _, _, err := f.svc.Build(f.ctx, plain, false); !errors.Is(err, store.ErrBuildInProgress) {
		t.Errorf("second build: %v", err)
	}
	if err := f.st.DeleteBootImage(f.ctx, plain); !errors.Is(err, store.ErrBuildInProgress) {
		t.Errorf("delete while building: %v", err)
	}
}

func TestDiff(t *testing.T) {
	base := Inputs{Recipe: 1, BootWIM: "w", SourceIndex: 1, Architecture: "x64", Builder: "wimlib", Agent: "a",
		ServerURLs: []string{"u"}, Bootmgr: "auto", TFTPBlockSize: 16384, TFTPWindowSize: 16,
		Drivers: []DriverInput{{"p2", "b.inf"}, {"p1", "a.inf"}}}
	cur := base
	cur.Builder, cur.Agent = "", "" // unknown: not compared
	cur.Drivers = []DriverInput{{"p1", "a.inf"}, {"p2", "b.inf"}}
	if r := Diff(base, cur); r != nil {
		t.Errorf("same inputs: %v", r)
	}
	if base.Hash() != (Inputs{Recipe: 1, BootWIM: "w", SourceIndex: 1, Architecture: "x64", Builder: "wimlib", Agent: "a",
		ServerURLs: []string{"u"}, Bootmgr: "auto", TFTPBlockSize: 16384, TFTPWindowSize: 16, Drivers: cur.Drivers}).Hash() {
		t.Error("driver order changes the hash")
	}
	cur = Inputs{Recipe: 2, BootWIM: "x", SourceIndex: 2, Architecture: "arm64", Builder: "dism", Agent: "b", ServerURLs: []string{"v"},
		Bootmgr: "windows-pca-2011", TFTPBlockSize: 1456, TFTPWindowSize: 16}
	want := []string{ReasonRecipe, ReasonSourceImage, ReasonSourceIndex, ReasonArchitecture, ReasonBuilder, ReasonAgent,
		ReasonServerURLs, ReasonBootmgr, ReasonTFTP, ReasonDrivers}
	if r := Diff(base, cur); !slices.Equal(r, want) {
		t.Errorf("all changed: %v", r)
	}
}

func TestFontName(t *testing.T) {
	for in, want := range map[string]string{"wgl4_boot_EX.ttf": "wgl4_boot.ttf", "chs_boot_EX.TTF": "chs_boot.TTF", "plain.ttf": "plain.ttf"} {
		if got := fontName(in, true); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if fontName("a_EX.ttf", false) != "a_EX.ttf" {
		t.Error("non-EX renamed")
	}
}

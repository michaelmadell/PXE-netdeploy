package bootimage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/agentbin"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/signer"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/wim"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Paths inside WinPE (X:\) and in the built tree (ADR 0022).
const (
	agentDir     = "netdeploy"
	winpeshlPath = "Windows/System32/winpeshl.ini"

	TreeBootmgr = "bootmgfw.efi"
	TreeBCD     = "Boot/BCD"
	TreeSDI     = "Boot/boot.sdi"
	TreeWIM     = "Boot/boot.wim"
	TreeFonts   = "EFI/Microsoft/Boot/Fonts"
)

// winpeshl launches only the agent; it loads staged drivers and runs
// wpeinit itself (ADR 0022). When it exits, WinPE restarts.
const winpeshl = "[LaunchApps]\r\n%SYSTEMDRIVE%\\netdeploy\\agent.exe\r\n"

// AgentConfig is X:\netdeploy\config.json (cmd/agent reads it).
type AgentConfig struct {
	ServerURLs []string `json:"serverUrls"`
	InputHash  string   `json:"inputHash"`
	// Drivers are INF paths relative to X:\netdeploy, loaded with drvload
	// before wpeinit (wimlib builds only).
	Drivers []string `json:"drivers"`
	// ContentCAPEM is the content listener's CA (Phase 6); empty until then.
	ContentCAPEM string `json:"contentCaPem"`
}

func (s *Service) build(ctx context.Context, r *jobs.Run, bi store.BootImage, p plan) error {
	log := r.Logger().With("boot_image_id", bi.ID, "builder", p.inputs.Builder)
	st, err := s.content.NewStaging(r.Job.ID)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			st.Discard()
		}
	}()
	if err := s.content.CheckSpace(4 * p.wimSize); err != nil {
		return err
	}
	work := filepath.Join(st.Dir(), "work")
	add := filepath.Join(work, "add")
	if err := os.MkdirAll(filepath.Join(add, agentDir), 0o700); err != nil {
		return err
	}

	r.Progress(2, fmt.Sprintf("Exporting boot.wim index %d", bi.SourceIndex))
	exported := filepath.Join(work, "export.wim")
	if err := p.tool.Export(ctx, p.bootWIM, bi.SourceIndex, exported); err != nil {
		return stepError(ctx, "export", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.Progress(35, "Adding the agent and drivers")
	infs, staged, err := s.materializeDrivers(ctx, p, work, add)
	if err != nil {
		return err
	}
	if err := writeAgentFiles(add, p.agent, AgentConfig{ServerURLs: bi.ServerURLs, InputHash: p.hash, Drivers: staged}); err != nil {
		return err
	}
	out := filepath.Join(work, "boot.wim")
	if err := p.tool.Customize(ctx, Customization{WIM: exported, Out: out, Work: work, Files: add, Drivers: infs, Log: log}); err != nil {
		return stepError(ctx, "customize", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.Progress(85, "Reading boot files")
	bf, err := s.readBootFiles(out, bi.Bootmgr)
	if err != nil {
		return err
	}
	bcd, err := s.bcd.WriteBCD(ctx, work, BCDOptions{Architecture: bi.Architecture, TFTPBlockSize: bi.TFTPBlockSize, TFTPWindowSize: bi.TFTPWindowSize})
	if err != nil {
		return fmt.Errorf("write BCD: %w", err)
	}

	r.Progress(92, "Saving")
	for _, f := range append([]treeFile{{TreeBootmgr, bf.bootmgr}, {TreeBCD, bcd}, {TreeSDI, bf.sdi}}, bf.fonts...) {
		if _, _, err := st.Add(f.path, bytes.NewReader(f.data)); err != nil {
			return err
		}
	}
	if _, _, err := st.AddFile(TreeWIM, out); err != nil {
		return err
	}
	sbm, _ := json.Marshal(map[string]any{
		"signer": bf.signer, "sourcePath": fmt.Sprintf("boot.wim:%d/%s", bi.SourceIndex, bf.source),
		"sha256": bf.bootmgrSHA, "architecture": bi.Architecture,
	})
	_, err = st.Commit(ctx, store.TreeBootImage, func(ctx context.Context, treeID string) error {
		return s.st.CompleteBootImageBuild(ctx, bi.ID, store.BootBuild{
			TreeID: treeID, InputHash: p.hash, Inputs: p.inputs.JSON(), BuiltWith: p.inputs.Builder,
			SelectedBootManager: sbm, Warnings: p.warnings,
		})
	})
	committed = true
	if err != nil {
		return err
	}
	r.Progress(100, fmt.Sprintf("Built with %s (%s boot manager)", p.inputs.Builder, bf.signer))
	log.Info("boot image built", "input_hash", p.hash, "drivers", len(p.drivers))
	return nil
}

// stepError keeps cancellation recognisable and names the failed step.
func stepError(ctx context.Context, step string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return jobs.Fail(http.StatusInternalServerError, "build_failed", "Build failed", step+": "+err.Error())
}

func writeAgentFiles(add string, agent agentbin.Binary, cfg AgentConfig) error {
	if cfg.Drivers == nil {
		cfg.Drivers = []string{}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(add, agentDir, "agent.exe"))
	if err != nil {
		return err
	}
	if _, err := f.ReadFrom(agent.Open()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(add, agentDir, "config.json"), b, 0o644); err != nil {
		return err
	}
	ini := filepath.Join(add, filepath.FromSlash(winpeshlPath))
	if err := os.MkdirAll(filepath.Dir(ini), 0o755); err != nil {
		return err
	}
	return os.WriteFile(ini, []byte(winpeshl), 0o644)
}

// materializeDrivers puts each enabled INF's directory (from its package
// tree) on disk: for DISM under work/drivers, returning the INF paths to
// inject; for wimlib under the added files at netdeploy\drivers, returning
// their WinPE-relative paths for the agent's drvload.
func (s *Service) materializeDrivers(ctx context.Context, p plan, work, add string) (infs, staged []string, err error) {
	root := filepath.Join(work, "drivers")
	if p.inputs.Builder == BuilderWimlib {
		root = filepath.Join(add, agentDir, "drivers")
	}
	dirs := map[string]string{} // package id + dir → local directory
	entries := map[string][]store.TreeEntry{}
	for _, d := range p.drivers {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		dir := path.Dir(d.Path)
		key := d.PackageID + "|" + dir
		local, ok := dirs[key]
		if !ok {
			local = filepath.Join(root, fmt.Sprint(len(dirs)+1))
			dirs[key] = local
			es, ok := entries[d.TreeID]
			if !ok {
				if es, err = s.st.TreeEntries(ctx, d.TreeID); err != nil {
					return nil, nil, err
				}
				entries[d.TreeID] = es
			}
			for _, e := range es {
				rel, ok := under(e.Path, dir)
				if !ok {
					continue
				}
				blob, err := s.content.BlobPath(e.SHA256)
				if err != nil {
					return nil, nil, err
				}
				if err := linkOrCopy(blob, filepath.Join(local, filepath.FromSlash(rel))); err != nil {
					return nil, nil, fmt.Errorf("stage driver file %s: %w", e.Path, err)
				}
			}
		}
		rel, _ := under(d.Path, dir)
		infs = append(infs, filepath.Join(local, filepath.FromSlash(rel)))
		if p.inputs.Builder == BuilderWimlib {
			staged = append(staged, `drivers\`+filepath.Base(local)+`\`+strings.ReplaceAll(rel, "/", `\`))
		}
	}
	if p.inputs.Builder == BuilderWimlib {
		return nil, staged, nil
	}
	return infs, nil, nil
}

// under returns p relative to dir ("." is the tree root), case-insensitively.
func under(p, dir string) (string, bool) {
	if dir == "." {
		return p, true
	}
	if len(p) > len(dir)+1 && strings.EqualFold(p[:len(dir)], dir) && p[len(dir)] == '/' {
		return p[len(dir)+1:], true
	}
	return "", false
}

type treeFile struct {
	path string
	data []byte
}

type bootFiles struct {
	bootmgr    []byte
	bootmgrSHA string
	signer     string
	source     string // path inside the WIM image
	sdi        []byte
	fonts      []treeFile
}

// Boot files inside a WinPE image (Phase 3 findings, ADR 0022).
const (
	wimBootmgr2011 = "Windows/Boot/EFI/bootmgfw.efi"
	wimBootmgr2023 = "Windows/Boot/EFI_EX/bootmgfw_EX.efi"
	wimSDI         = "Windows/Boot/DVD/EFI/boot.sdi"
	wimFonts       = "Windows/Boot/Fonts"
	wimFontsEX     = "Windows/Boot/Fonts_EX"
)

// readBootFiles reads the boot manager chosen by want, boot.sdi and the
// fonts out of the built WIM.
func (s *Service) readBootFiles(wimPath, want string) (bootFiles, error) {
	f, err := os.Open(wimPath)
	if err != nil {
		return bootFiles{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return bootFiles{}, err
	}
	rd, err := wim.Open(f, fi.Size())
	if err != nil {
		return bootFiles{}, fmt.Errorf("read built boot.wim: %w", err)
	}
	var bf bootFiles
	for _, c := range []struct{ path, signer string }{
		{wimBootmgr2023, string(signer.WindowsUEFICA2023)},
		{wimBootmgr2011, string(signer.WindowsPCA2011)},
	} {
		if want != "auto" && want != c.signer {
			continue
		}
		b, err := rd.ReadFile(1, c.path)
		if err != nil {
			continue
		}
		if s.classify(b) != c.signer {
			continue
		}
		bf.bootmgr, bf.signer, bf.source = b, c.signer, c.path
		break
	}
	if bf.bootmgr == nil {
		return bf, unprocessable("bootmgr_unavailable", "Boot manager unavailable",
			fmt.Sprintf("The built image has no boot manager signed %s.", want))
	}
	bf.bootmgrSHA = sha256Hex(bf.bootmgr)
	if bf.sdi, err = rd.ReadFile(1, wimSDI); err != nil {
		return bf, unprocessable("boot_files_missing", "Boot files missing", "The image has no "+wimSDI+".")
	}
	fontDir, ex := wimFonts, false
	if bf.signer == string(signer.WindowsUEFICA2023) {
		if _, err := rd.ReadDir(1, wimFontsEX); err == nil {
			fontDir, ex = wimFontsEX, true
		}
	}
	ents, err := rd.ReadDir(1, fontDir)
	if err != nil {
		return bf, unprocessable("boot_files_missing", "Boot files missing", "The image has no "+fontDir+".")
	}
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".ttf") {
			continue
		}
		b, err := rd.ReadFile(1, fontDir+"/"+e.Name())
		if err != nil {
			return bf, fmt.Errorf("read font %s: %w", e.Name(), err)
		}
		bf.fonts = append(bf.fonts, treeFile{path: TreeFonts + "/" + fontName(e.Name(), ex), data: b})
	}
	return bf, nil
}

// fontName drops the _EX suffix of the 2023 font set (wgl4_boot_EX.ttf →
// wgl4_boot.ttf): Boot Manager asks for the plain names.
func fontName(name string, ex bool) string {
	if !ex {
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if strings.HasSuffix(strings.ToUpper(base), "_EX") {
		base = base[:len(base)-3]
	}
	return base + ext
}

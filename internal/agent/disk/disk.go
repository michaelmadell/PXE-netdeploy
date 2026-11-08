// Package disk finds the target disk and partitions it (ADR 0025).
package disk

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Disk is a physical disk as the agent sees it.
type Disk struct {
	Number    int
	SizeBytes int64
	Bus       string // NVMe, SATA, RAID, SAS, USB, …
	Model     string
}

func (d Disk) String() string {
	return fmt.Sprintf("disk %d: %.1f GB %s %s", d.Number, float64(d.SizeBytes)/1e9, d.Bus, d.Model)
}

// MinSize is the smallest disk auto-selection considers.
const MinSize = 32 << 30

// excludedBus are buses auto-selection never picks.
var excludedBus = map[string]bool{"USB": true, "SD": true, "MMC": true}

// Select picks the target disk (ADR 0025): explicit, if given and present;
// else the lowest-numbered disk that isn't USB, SD or MMC and has at least
// MinSize bytes.
func Select(disks []Disk, explicit *int) (Disk, error) {
	if explicit != nil {
		for _, d := range disks {
			if d.Number == *explicit {
				return d, nil
			}
		}
		return Disk{}, fmt.Errorf("the plan names disk %d, which isn't present", *explicit)
	}
	best := -1
	for i, d := range disks {
		if excludedBus[d.Bus] || d.SizeBytes < MinSize {
			continue
		}
		if best < 0 || d.Number < disks[best].Number {
			best = i
		}
	}
	if best < 0 {
		var seen []string
		for _, d := range disks {
			seen = append(seen, d.String())
		}
		if len(seen) == 0 {
			seen = []string{"none"}
		}
		return Disk{}, fmt.Errorf("no disk to install to: need one that isn't USB/SD/MMC with at least %d GB (found: %s)",
			MinSize>>30, strings.Join(seen, "; "))
	}
	return disks[best], nil
}

// Wait polls list until its result has been the same for settle (storage
// drivers loaded with drvload expose their disks a little later), or until
// max, and returns it.
func Wait(list func() []Disk, settle, max time.Duration) []Disk {
	start := time.Now()
	last := fmt.Sprint(list())
	since := time.Now()
	for time.Since(start) < max && time.Since(since) < settle {
		time.Sleep(time.Second)
		if cur := fmt.Sprint(list()); cur != last {
			last, since = cur, time.Now()
		}
	}
	disks := list()
	slog.Info("disks", "count", len(disks), "waited", time.Since(start).Round(time.Second))
	for _, d := range disks {
		slog.Info(d.String())
	}
	return disks
}

// Letters the layout assigns.
const (
	SystemLetter   = "S"
	WindowsLetter  = "W"
	RecoveryLetter = "R"
)

// Script is the diskpart script for Microsoft's UEFI/GPT layout on disk n:
// EFI 260 MB FAT32 (S:), MSR 16 MB, Windows NTFS (W:), recovery 1 GB at the
// end (R:, the WinRE type and attributes).
func Script(n int) string {
	return strings.Join([]string{
		fmt.Sprintf("select disk %d", n),
		"clean",
		"convert gpt",
		"create partition efi size=260",
		`format quick fs=fat32 label="System"`,
		`assign letter="` + SystemLetter + `"`,
		"create partition msr size=16",
		"create partition primary",
		"shrink minimum=1024",
		`format quick fs=ntfs label="Windows"`,
		`assign letter="` + WindowsLetter + `"`,
		"create partition primary",
		`format quick fs=ntfs label="Recovery"`,
		`assign letter="` + RecoveryLetter + `"`,
		`set id="de94bba4-06d1-4d40-a16a-bfd50179d6ac"`,
		"gpt attributes=0x8000000000000001",
		"list volume",
		"exit",
	}, "\r\n") + "\r\n"
}

package main

import (
	"fmt"
	"log/slog"
	"time"
)

// disk is a physical disk as the agent sees it.
type disk struct {
	Number    int
	SizeBytes int64
	Bus       string
	Model     string
}

func (d disk) String() string {
	return fmt.Sprintf("disk %d: %.1f GB %s %s", d.Number, float64(d.SizeBytes)/1e9, d.Bus, d.Model)
}

// waitForDisks polls the disk list until it has been the same for settle
// (controllers whose drivers drvload just loaded, such as RAID or VMD,
// enumerate their disks a few seconds later), or until max, and returns it.
func waitForDisks(settle, max time.Duration) []disk {
	start := time.Now()
	last := fmt.Sprint(listDisks())
	since := time.Now()
	for time.Since(start) < max && time.Since(since) < settle {
		time.Sleep(time.Second)
		if cur := fmt.Sprint(listDisks()); cur != last {
			last, since = cur, time.Now()
		}
	}
	disks := listDisks()
	slog.Info("disks", "count", len(disks), "waited", time.Since(start).Round(time.Second))
	for _, d := range disks {
		slog.Info(d.String())
	}
	return disks
}

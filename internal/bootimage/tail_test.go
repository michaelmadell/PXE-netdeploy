package bootimage

import (
	"strings"
	"testing"
)

func TestTailDropsProgress(t *testing.T) {
	out := "Scanning \"C:\\x\\add\"\r\n10 MiB scanned (1 files, 0 directories)\r10 MiB scanned (3 files, 4 directories)\r\n" +
		"Using LZX compression with 14 threads\r\nArchiving file data: 0 MiB of 1239 MiB (0%) done\rArchiving file data: 1239 MiB of 1239 MiB (100%) done\r\n" +
		"[ERROR] Failed to rename `a' to `b': Resource busy\r\nERROR: Exiting with error code 52:\r\n       Could not rename a file.\r\n"
	got := tail([]byte(out), 3)
	if got != "[ERROR] Failed to rename `a' to `b': Resource busy | ERROR: Exiting with error code 52: | Could not rename a file." {
		t.Errorf("%q", got)
	}
	if strings.Contains(tail([]byte(out), 12), "% done") {
		t.Error("progress kept")
	}
}

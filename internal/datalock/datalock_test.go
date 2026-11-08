package datalock

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

// The lock is per open file, so a second Acquire in this process fails too
// (Windows LockFileEx and Linux flock on a new descriptor both refuse).
func TestAcquire(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire: %v", err)
	}
	l.Release()
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
}

// Another process can't take it either.
func TestAcquireOtherProcess(t *testing.T) {
	if dir := os.Getenv("DATALOCK_CHILD"); dir != "" {
		if _, err := Acquire(dir); errors.Is(err, ErrLocked) {
			os.Exit(3)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	cmd := exec.Command(os.Args[0], "-test.run=TestAcquireOtherProcess")
	cmd.Env = append(os.Environ(), "DATALOCK_CHILD="+dir)
	err = cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("child: %v", err)
	}
}

//go:build !windows

package firmware

import "errors"

// Windows is only available on Windows.
type Windows struct{}

// Open fails off Windows.
func Open() (Windows, error) { return Windows{}, errors.New("firmware variables need Windows") }

func (Windows) Get(string) ([]byte, error) { return nil, ErrNotFound }
func (Windows) Set(string, []byte) error   { return errors.New("firmware variables need Windows") }

// VolumePartition fails off Windows.
func VolumePartition(string) (Partition, error) {
	return Partition{}, errors.New("volume partitions need Windows")
}

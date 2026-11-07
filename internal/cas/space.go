package cas

import (
	"context"
	"fmt"
)

// Space describes the content store's volume.
type Space struct {
	Total   int64 // size of the volume
	Free    int64 // available to this process
	Content int64 // stored blobs, each counted once
	Reserve int64 // storage.reserveBytes
}

// Low reports free space below twice the reserve (the storage_low warning).
func (sp Space) Low() bool { return sp.Free < 2*sp.Reserve }

// Space reports the volume's size and free space and the store's usage.
func (s *Store) Space(ctx context.Context) (Space, error) {
	total, free, err := s.diskSpace(s.root)
	if err != nil {
		return Space{}, fmt.Errorf("cas: disk space: %w", err)
	}
	content, err := s.db.ContentBytes(ctx)
	if err != nil {
		return Space{}, err
	}
	return Space{Total: total, Free: free, Content: content, Reserve: s.reserve()}, nil
}

// CheckSpace returns ErrInsufficientStorage if writing need more bytes would
// leave less free space than the reserve.
func (s *Store) CheckSpace(need int64) error {
	_, free, err := s.diskSpace(s.root)
	if err != nil {
		return fmt.Errorf("cas: disk space: %w", err)
	}
	if reserve := s.reserve(); free-need < reserve {
		return fmt.Errorf("%w: %d bytes free, %d needed plus a reserve of %d", ErrInsufficientStorage, free, need, reserve)
	}
	return nil
}

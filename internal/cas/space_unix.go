//go:build unix

package cas

import "golang.org/x/sys/unix"

func diskSpace(path string) (total, free int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := int64(st.Bsize) //nolint:unconvert // int32 on some platforms
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs, nil
}

//go:build !unix

package termimg

import "errors"

// CellSizeFromFD is not supported on this platform; use [QueryCellSize] or
// [DefaultCellSize].
func CellSizeFromFD(fd uintptr) (CellSize, error) {
	return CellSize{}, errors.New("termimg: cell size ioctl not supported on this platform")
}

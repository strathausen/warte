//go:build unix

package termimg

import (
	"errors"

	"golang.org/x/sys/unix"
)

// CellSizeFromFD reads the cell size in pixels from the terminal attached to
// fd using the TIOCGWINSZ ioctl (ws_xpixel / ws_col, ws_ypixel / ws_row).
// Many terminals report zero pixel sizes; then an error is returned and
// callers should fall back to [QueryCellSize] or [DefaultCellSize].
func CellSizeFromFD(fd uintptr) (CellSize, error) {
	ws, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil {
		return CellSize{}, err
	}
	return cellSizeFromWinsize(ws.Col, ws.Row, ws.Xpixel, ws.Ypixel)
}

func cellSizeFromWinsize(cols, rows, xpix, ypix uint16) (CellSize, error) {
	if cols == 0 || rows == 0 || xpix == 0 || ypix == 0 {
		return CellSize{}, errors.New("termimg: terminal does not report pixel size")
	}
	return CellSize{Width: int(xpix) / int(cols), Height: int(ypix) / int(rows)}, nil
}

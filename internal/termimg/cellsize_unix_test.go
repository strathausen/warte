//go:build unix

package termimg

import (
	"os"
	"testing"
)

func TestCellSizeFromWinsize(t *testing.T) {
	tests := []struct {
		cols, rows, xpix, ypix uint16
		want                   CellSize
		wantErr                bool
	}{
		{80, 24, 800, 480, CellSize{10, 20}, false},
		{100, 50, 900, 900, CellSize{9, 18}, false},
		{80, 24, 0, 0, CellSize{}, true},
		{0, 0, 800, 480, CellSize{}, true},
	}
	for _, tt := range tests {
		got, err := cellSizeFromWinsize(tt.cols, tt.rows, tt.xpix, tt.ypix)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("cellSizeFromWinsize(%d,%d,%d,%d) = %v, %v", tt.cols, tt.rows, tt.xpix, tt.ypix, got, err)
		}
	}
}

func TestCellSizeFromFDNotATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := CellSizeFromFD(f.Fd()); err == nil {
		t.Fatal("expected error for a regular file")
	}
}

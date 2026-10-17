//go:build !unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// acquireLock creates path exclusively; an existing file means another
// process (or a crashed one: delete the file) holds the data directory.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, errLocked
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

func releaseLock(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

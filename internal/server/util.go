package server

import (
	"os"
	"time"
)

func fileModTime(f *os.File) time.Time {
	if fi, err := f.Stat(); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// Package fsx holds small filesystem helpers shared by laman's writers.
package fsx

import (
	"os"
	"time"
)

const (
	renameTries = 5
	renameWait  = 25 * time.Millisecond
)

// Rename is os.Rename that, on Windows, retries a few times (about 100 ms in
// all) when another process briefly holds the target open without
// FILE_SHARE_DELETE (a sharing violation or access denied).
func Rename(from, to string) error {
	var err error
	for i := 0; i < renameTries; i++ {
		if err = os.Rename(from, to); err == nil || !transient(err) {
			return err
		}
		if i < renameTries-1 {
			time.Sleep(renameWait)
		}
	}
	return err
}

package daemon

import (
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Project sizes for the site detail page, measured in the background:
// walking a node_modules can take seconds, so a request never waits for it.

type dirSizes struct {
	Total       int64 `json:"total"`
	Vendor      int64 `json:"vendor"`      // the top-level vendor folder
	NodeModules int64 `json:"nodeModules"` // the top-level node_modules folder
	// Whether those folders existed when measured, to notice an install
	// done outside the dashboard.
	HasVendor      bool `json:"-"`
	HasNodeModules bool `json:"-"`
}

type sizeState struct {
	State string    `json:"state"` // measuring, done, error
	Sizes dirSizes  `json:"sizes"`
	At    time.Time `json:"at,omitzero"`
}

// measureDir adds up the files under root. Symlinks and junctions are not
// followed (pnpm's node_modules is full of them, and they can loop), and
// files that can't be read are skipped.
func measureDir(root string) (dirSizes, error) {
	vendor := filepath.Join(root, "vendor") + string(filepath.Separator)
	nodeModules := filepath.Join(root, "node_modules") + string(filepath.Separator)
	s := dirSizes{HasVendor: exists(vendor), HasNodeModules: exists(nodeModules)}
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if e != nil && e.IsDir() && p != root {
				return fs.SkipDir
			}
			return nil
		}
		// Real folders are walked; links and junctions (ModeSymlink,
		// ModeIrregular) are neither folders nor regular files here.
		if !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		n := info.Size()
		s.Total += n
		switch {
		case strings.HasPrefix(p, vendor):
			s.Vendor += n
		case strings.HasPrefix(p, nodeModules):
			s.NodeModules += n
		}
		return nil
	})
	return s, err
}

// measure is measureDir; tests swap it.
var measure = measureDir

// sizeCache holds one measurement per project folder. The zero value is
// ready to use. At most two folders are walked at once.
type sizeCache struct {
	mu      sync.Mutex
	entries map[string]*sizeState
	running map[string]bool
	sem     chan struct{}
}

// get returns the folder's last measurement, starting the first one.
func (c *sizeCache) get(root string) sizeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[root]; e != nil {
		return *e
	}
	c.startLocked(root)
	return sizeState{State: "measuring"}
}

// refresh measures the folder again, unless a measurement is running.
func (c *sizeCache) refresh(root string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startLocked(root)
}

// forget drops the folder's measurement (the site is gone).
func (c *sizeCache) forget(root string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, root)
}

func (c *sizeCache) startLocked(root string) {
	if c.running[root] {
		return
	}
	if c.entries == nil {
		c.entries, c.running, c.sem = map[string]*sizeState{}, map[string]bool{}, make(chan struct{}, 2)
	}
	c.running[root] = true
	if e := c.entries[root]; e != nil {
		e.State = "measuring" // keep the old numbers on show meanwhile
	}
	run := measure
	go func() {
		c.sem <- struct{}{}
		s, err := run(root)
		<-c.sem
		st := &sizeState{State: "done", Sizes: s, At: time.Now()}
		if err != nil {
			st.State = "error"
		}
		c.mu.Lock()
		c.entries[root] = st
		delete(c.running, root)
		c.mu.Unlock()
	}()
}

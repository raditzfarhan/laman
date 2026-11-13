package laragon

import (
	"os"
	"path/filepath"
	"strings"

	"laman/internal/config"
)

// The import records the database it is copying in this file under laman's
// logs and removes it once Copy returns. If the import is killed meanwhile
// (laman quit or updated), the database may be left half-loaded in laman,
// where it then looks "already in laman"; the next plan says so.
const copyingName = "import-laragon.copying"

func copyingPath() string { return filepath.Join(config.LogDir(), copyingName) }

// MarkCopying records that db is being copied.
func MarkCopying(db string) error {
	if err := os.MkdirAll(config.LogDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(copyingPath(), []byte(db+"\n"), 0o644)
}

// ClearCopying removes the record MarkCopying wrote.
func ClearCopying() { os.Remove(copyingPath()) }

// InterruptedCopy is the database an earlier import was copying when it
// was killed, when laman has a database by that name (lamanDBs); "" when
// there is none.
func InterruptedCopy(lamanDBs []string) string {
	b, err := os.ReadFile(copyingPath())
	if err != nil {
		return ""
	}
	db := strings.TrimSpace(string(b))
	for _, n := range lamanDBs {
		if db != "" && strings.EqualFold(n, db) {
			return n
		}
	}
	return ""
}

// NoteInterrupted adds a note to p when an earlier import was killed while
// copying a database laman now has. Nothing is dropped: that is the user's call.
func NoteInterrupted(p *Plan, lamanDBs []string) string {
	db := InterruptedCopy(lamanDBs)
	if db != "" {
		p.Notes = append(p.Notes, db+" may be incomplete from an interrupted import — drop it in laman and import again.")
	}
	return db
}

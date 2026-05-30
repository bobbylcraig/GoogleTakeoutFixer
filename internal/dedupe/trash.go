/*
GoogleTakeoutFixer - A tool to easily clean and organize Google Photos Takeout exports
Copyright (C) 2026 feloex

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package dedupe

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// manifestName is the restore log kept inside the trash folder. It records
// where every trashed file came from so a deletion is never truly destructive.
const manifestName = "restore-manifest.json"

// TrashEntry records one moved file: where it lives now and where it came from.
type TrashEntry struct {
	TrashedPath  string    `json:"trashed_path"`
	OriginalPath string    `json:"original_path"`
	TrashedAt    time.Time `json:"trashed_at"`
}

// Trash moves files into a trash directory rather than deleting them, and
// appends each move to a manifest so the operation can be undone. It is safe
// for concurrent use; the reviewer may trash several files in quick succession.
type Trash struct {
	dir string

	mu      sync.Mutex
	entries []TrashEntry
}

// NewTrash prepares (creating if needed) a trash directory and loads any
// existing manifest so restores work across sessions.
func NewTrash(dir string) (*Trash, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	t := &Trash{dir: dir}
	if data, err := os.ReadFile(filepath.Join(dir, manifestName)); err == nil {
		_ = json.Unmarshal(data, &t.entries) // a corrupt manifest just starts empty
	}
	return t, nil
}

// Dir returns the trash directory path.
func (t *Trash) Dir() string { return t.dir }

// Move relocates src into the trash, preserving its base name (disambiguating
// collisions with a numeric suffix) and recording the original location.
func (t *Trash) Move(src string) (TrashEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	dest := t.uniqueDestLocked(filepath.Base(src))
	if err := moveFile(src, dest); err != nil {
		return TrashEntry{}, err
	}

	entry := TrashEntry{TrashedPath: dest, OriginalPath: src, TrashedAt: time.Now()}
	t.entries = append(t.entries, entry)
	if err := t.writeManifestLocked(); err != nil {
		return entry, err
	}
	return entry, nil
}

// Restore moves a previously trashed file back to its original location and
// drops it from the manifest. It refuses to clobber a file that has since
// reappeared at the original path.
func (t *Trash) Restore(trashedPath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	idx := -1
	for i, e := range t.entries {
		if e.TrashedPath == trashedPath {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no manifest entry for %s", trashedPath)
	}
	entry := t.entries[idx]

	if _, err := os.Stat(entry.OriginalPath); err == nil {
		return fmt.Errorf("cannot restore: %s already exists", entry.OriginalPath)
	}
	if err := os.MkdirAll(filepath.Dir(entry.OriginalPath), 0755); err != nil {
		return err
	}
	if err := moveFile(entry.TrashedPath, entry.OriginalPath); err != nil {
		return err
	}

	t.entries = append(t.entries[:idx], t.entries[idx+1:]...)
	return t.writeManifestLocked()
}

// Entries returns a copy of the current manifest entries.
func (t *Trash) Entries() []TrashEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TrashEntry, len(t.entries))
	copy(out, t.entries)
	return out
}

// uniqueDestLocked picks a non-colliding path inside the trash dir. Caller
// holds t.mu.
func (t *Trash) uniqueDestLocked(base string) string {
	dest := filepath.Join(t.dir, base)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return dest
	}
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	for i := 1; ; i++ {
		candidate := filepath.Join(t.dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// writeManifestLocked persists the manifest. Caller holds t.mu.
func (t *Trash) writeManifestLocked() error {
	data, err := json.MarshalIndent(t.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(t.dir, manifestName), data, 0644)
}

// moveFile renames src to dest, falling back to copy+remove when the two are on
// different filesystems (os.Rename fails with EXDEV across mounts).
func moveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	return os.Remove(src)
}

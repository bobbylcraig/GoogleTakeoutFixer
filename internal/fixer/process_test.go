package fixer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// drainProgress consumes the progress channel so Process does not block.
func drainProgress(ch <-chan Progress, done chan<- struct{}) {
	for range ch {
	}
	close(done)
}

// collectProgress drains the channel, recording the final progress event so a
// test can assert the run reached its expected total.
func collectProgress(ch <-chan Progress, last *Progress, done chan<- struct{}) {
	for p := range ch {
		*last = p
	}
	close(done)
}

// TestProcess_LivePhotoPairProgressCountsBothHalves verifies that processing a
// Live Photo pair advances the progress counter for both the still and the
// video, so the reported total matches the number of source media files.
func TestProcess_LivePhotoPairProgressCountsBothHalves(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()

	year := filepath.Join(src, "Photos from 2020")
	os.MkdirAll(year, 0755)
	// One Live Photo pair (2 files) plus one standalone photo (1 file) = 3.
	writeFile(t, filepath.Join(year, "IMG_0001.heic"), "still")
	writeFile(t, filepath.Join(year, "IMG_0001.mov"), "motion")
	writeFile(t, filepath.Join(year, "IMG_0002.jpg"), "standalone")

	ch := make(chan Progress, 64)
	done := make(chan struct{})
	var last Progress
	go collectProgress(ch, &last, done)

	opts := ProcessOptions{Flatten: true, WriteMetadata: false}
	if err := Process(context.Background(), src, out, ch, opts); err != nil {
		t.Fatalf("Process: %v", err)
	}
	<-done

	if last.Total != 3 {
		t.Fatalf("expected Total 3 source files, got %d", last.Total)
	}
	if last.Processed != 3 {
		t.Fatalf("expected Processed to reach 3 (pair counts as 2 + 1 standalone), got %d", last.Processed)
	}
}

// TestProcess_SameNameDifferentContentBothKept verifies that two different
// files sharing a name (the Google Takeout "(1)" duplicate case) both land in
// the output rather than one being silently skipped (issue #6 / #28).
func TestProcess_SameNameDifferentContentBothKept(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()

	// Two year folders each containing a distinct photo with the same name.
	a := filepath.Join(src, "Photos from 2020")
	b := filepath.Join(src, "Photos from 2021")
	if err := os.MkdirAll(a, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a, "IMG_0001.jpg"), "photo-A-bytes")
	writeFile(t, filepath.Join(b, "IMG_0001.jpg"), "photo-B-bytes-different")

	ch := make(chan Progress, 64)
	done := make(chan struct{})
	go drainProgress(ch, done)

	// Flatten so both files target the same output directory, forcing a collision.
	// WriteMetadata off so the test needs no exiftool binary.
	opts := ProcessOptions{Flatten: true, WriteMetadata: false}
	if err := Process(context.Background(), src, out, ch, opts); err != nil {
		t.Fatalf("Process: %v", err)
	}
	<-done

	first := filepath.Join(out, "IMG_0001.jpg")
	second := filepath.Join(out, "IMG_0001 (1).jpg")
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("expected %s to exist: %v", first, err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("expected collision-renamed %s to exist: %v", second, err)
	}

	// The two output files must carry the two distinct source contents.
	c1, _ := os.ReadFile(first)
	c2, _ := os.ReadFile(second)
	got := map[string]bool{string(c1): true, string(c2): true}
	if !got["photo-A-bytes"] || !got["photo-B-bytes-different"] {
		t.Fatalf("output contents do not match both sources: %q and %q", c1, c2)
	}
}

// TestDuplicateFile_FailedCopyRemovesPartial verifies a failed copy does not
// leave a partial/placeholder file behind, which a re-run would otherwise
// mistake for a real colliding file.
func TestDuplicateFile_FailedCopyRemovesPartial(t *testing.T) {
	dir := t.TempDir()
	// Source is a directory: os.Open succeeds but io.Copy from it fails.
	badSrc := filepath.Join(dir, "src.jpg")
	if err := os.MkdirAll(badSrc, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out.jpg")

	if err := DuplicateFile(badSrc, dest); err == nil {
		t.Fatal("expected DuplicateFile to fail copying from a directory")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("partial output file was left behind after a failed copy")
	}
}

// TestProcess_IdenticalDuplicateSkipped verifies a byte-identical duplicate is
// not written twice.
func TestProcess_IdenticalDuplicateSkipped(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()

	a := filepath.Join(src, "Photos from 2020")
	b := filepath.Join(src, "Photos from 2021")
	os.MkdirAll(a, 0755)
	os.MkdirAll(b, 0755)
	writeFile(t, filepath.Join(a, "IMG_0002.jpg"), "identical")
	writeFile(t, filepath.Join(b, "IMG_0002.jpg"), "identical")

	ch := make(chan Progress, 64)
	done := make(chan struct{})
	go drainProgress(ch, done)

	opts := ProcessOptions{Flatten: true, WriteMetadata: false}
	if err := Process(context.Background(), src, out, ch, opts); err != nil {
		t.Fatalf("Process: %v", err)
	}
	<-done

	if _, err := os.Stat(filepath.Join(out, "IMG_0002.jpg")); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "IMG_0002 (1).jpg")); !os.IsNotExist(err) {
		t.Fatal("identical duplicate should not have produced a (1) variant")
	}
}

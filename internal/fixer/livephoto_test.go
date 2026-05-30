package fixer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectLivePhotoPairs(t *testing.T) {
	dir := t.TempDir()
	// A Live Photo pair, a standalone photo, a standalone video.
	writeFile(t, filepath.Join(dir, "IMG_0001.HEIC"), "still")
	writeFile(t, filepath.Join(dir, "IMG_0001.MOV"), "motion")
	writeFile(t, filepath.Join(dir, "IMG_0002.jpg"), "lonely-photo")
	writeFile(t, filepath.Join(dir, "CLIP.mp4"), "lonely-video")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	pairs := DetectLivePhotoPairs(dir, entries)

	if len(pairs) != 1 {
		t.Fatalf("expected exactly 1 pair, got %d: %v", len(pairs), pairs)
	}
	img := filepath.Join(dir, "IMG_0001.HEIC")
	vid := filepath.Join(dir, "IMG_0001.MOV")
	if pairs[img] != vid {
		t.Fatalf("expected %s -> %s, got %v", img, vid, pairs)
	}
}

func TestDetectLivePhotoPairs_NoVideoNoPair(t *testing.T) {
	dir := t.TempDir()
	// Two stills sharing a base but no video — must not pair.
	writeFile(t, filepath.Join(dir, "A.jpg"), "x")
	writeFile(t, filepath.Join(dir, "A.png"), "y")

	entries, _ := os.ReadDir(dir)
	if pairs := DetectLivePhotoPairs(dir, entries); len(pairs) != 0 {
		t.Fatalf("expected no pairs, got %v", pairs)
	}
}

func TestResolvePairDestPaths_SharedSuffixOnCollision(t *testing.T) {
	dir := t.TempDir()
	// Different files already occupy the unsuffixed names, forcing a suffix.
	writeFile(t, filepath.Join(dir, "IMG.heic"), "existing-image")
	writeFile(t, filepath.Join(dir, "IMG.mov"), "existing-video")

	imgSrc := filepath.Join(dir, "src.heic")
	vidSrc := filepath.Join(dir, "src.mov")
	writeFile(t, imgSrc, "new-image")
	writeFile(t, vidSrc, "new-video")

	imgDest := filepath.Join(dir, "IMG.heic")
	vidDest := filepath.Join(dir, "IMG.mov")
	imgOut, vidOut, skipI, skipV, err := ResolvePairDestPaths(imgSrc, vidSrc, imgDest, vidDest)
	if err != nil {
		t.Fatal(err)
	}
	if skipI || skipV {
		t.Fatal("distinct files should not be skipped")
	}
	// Both halves must carry the same suffix so their base names stay aligned.
	if filepath.Base(imgOut) != "IMG (1).heic" || filepath.Base(vidOut) != "IMG (1).mov" {
		t.Fatalf("expected shared (1) suffix, got %s + %s", filepath.Base(imgOut), filepath.Base(vidOut))
	}
}

// If only ONE half collides, the pair must still advance to the next slot
// TOGETHER, so the names never drift apart.
func TestResolvePairDestPaths_OneHalfCollidesBumpsBoth(t *testing.T) {
	dir := t.TempDir()
	// Only the image name is taken by a different file; video name is free.
	writeFile(t, filepath.Join(dir, "IMG.heic"), "existing-different-image")

	imgSrc := filepath.Join(dir, "src.heic")
	vidSrc := filepath.Join(dir, "src.mov")
	writeFile(t, imgSrc, "new-image")
	writeFile(t, vidSrc, "new-video")

	imgOut, vidOut, _, _, err := ResolvePairDestPaths(imgSrc, vidSrc,
		filepath.Join(dir, "IMG.heic"), filepath.Join(dir, "IMG.mov"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(imgOut) != "IMG (1).heic" || filepath.Base(vidOut) != "IMG (1).mov" {
		t.Fatalf("a single-side collision must bump both halves; got %s + %s",
			filepath.Base(imgOut), filepath.Base(vidOut))
	}
}

func TestResolvePairDestPaths_IdenticalSkipped(t *testing.T) {
	dir := t.TempDir()
	imgSrc := filepath.Join(dir, "src.heic")
	vidSrc := filepath.Join(dir, "src.mov")
	writeFile(t, imgSrc, "same-image")
	writeFile(t, vidSrc, "same-video")

	imgDest := filepath.Join(dir, "OUT.heic")
	vidDest := filepath.Join(dir, "OUT.mov")
	writeFile(t, imgDest, "same-image")
	writeFile(t, vidDest, "same-video")

	_, _, skipI, skipV, err := ResolvePairDestPaths(imgSrc, vidSrc, imgDest, vidDest)
	if err != nil {
		t.Fatal(err)
	}
	if !skipI || !skipV {
		t.Fatalf("byte-identical pair should be skipped on both halves; skipI=%v skipV=%v", skipI, skipV)
	}
}

// TestProcess_LivePhotoPairKeptTogether verifies that when two different Live
// Photo pairs share a base name (across year folders, flattened to one output),
// each pair keeps its two halves under a single matching suffix.
func TestProcess_LivePhotoPairKeptTogether(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()

	a := filepath.Join(src, "Photos from 2020")
	b := filepath.Join(src, "Photos from 2021")
	os.MkdirAll(a, 0755)
	os.MkdirAll(b, 0755)

	// Pair 1 in 2020.
	writeFile(t, filepath.Join(a, "IMG_0001.heic"), "image-A")
	writeFile(t, filepath.Join(a, "IMG_0001.mov"), "video-A")
	// Pair 2 in 2021 with the SAME base name but different content.
	writeFile(t, filepath.Join(b, "IMG_0001.heic"), "image-B-different")
	writeFile(t, filepath.Join(b, "IMG_0001.mov"), "video-B-different")

	ch := make(chan Progress, 64)
	done := make(chan struct{})
	go drainProgress(ch, done)

	opts := ProcessOptions{Flatten: true, WriteMetadata: false}
	if err := Process(context.Background(), src, out, ch, opts); err != nil {
		t.Fatalf("Process: %v", err)
	}
	<-done

	// One pair keeps the bare names; the other gets a shared (1) suffix.
	mustExist := func(name string) {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}
	mustExist("IMG_0001.heic")
	mustExist("IMG_0001.mov")
	mustExist("IMG_0001 (1).heic")
	mustExist("IMG_0001 (1).mov")

	// The suffixed pair's two halves must come from the SAME source pair, i.e.
	// the heic and mov contents belong together (both A, or both B).
	readStr := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(out, name))
		return string(data)
	}
	bareImg, bareVid := readStr("IMG_0001.heic"), readStr("IMG_0001.mov")
	sufImg, sufVid := readStr("IMG_0001 (1).heic"), readStr("IMG_0001 (1).mov")

	pairOK := func(img, vid string) bool {
		return (img == "image-A" && vid == "video-A") ||
			(img == "image-B-different" && vid == "video-B-different")
	}
	if !pairOK(bareImg, bareVid) {
		t.Fatalf("bare pair halves mismatched: %q + %q", bareImg, bareVid)
	}
	if !pairOK(sufImg, sufVid) {
		t.Fatalf("suffixed pair halves mismatched: %q + %q", sufImg, sufVid)
	}
}

package fixer

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsMotionPhotoStill(t *testing.T) {
	yes := []string{"a.jpg", "a.JPEG", "b.heic", "c.HEIF"}
	no := []string{"a.png", "a.mov", "a.mp4", "a.gif"}
	for _, p := range yes {
		if !IsMotionPhotoStill(p) {
			t.Errorf("expected %s to be a motion photo still", p)
		}
	}
	for _, p := range no {
		if IsMotionPhotoStill(p) {
			t.Errorf("expected %s NOT to be a motion photo still", p)
		}
	}
}

// fakeVideo is arbitrary trailer bytes standing in for an MP4; the muxer treats
// the trailer opaquely, so real video frames are unnecessary for these tests.
var fakeVideo = []byte("FAKE-MP4-VIDEO-TRAILER-BYTES-0123456789")

// TestMuxMotionPhoto_JPEG verifies the JPEG path: the video is appended
// directly and exiftool recognizes the result as a Motion Photo whose
// Item:Length points at the trailer.
func TestMuxMotionPhoto_JPEG(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed; skipping mux test")
	}
	if err := InitializeExifTool(); err != nil {
		t.Fatalf("init exiftool: %v", err)
	}
	defer CloseExifTool()

	dir := t.TempDir()
	still := filepath.Join(dir, "live.jpg")
	video := filepath.Join(dir, "live.mp4")
	if err := writeBytes(still, tinyJPEG); err != nil {
		t.Fatal(err)
	}
	if err := writeBytes(video, fakeVideo); err != nil {
		t.Fatal(err)
	}

	if err := MuxMotionPhoto(still, video); err != nil {
		t.Fatalf("MuxMotionPhoto: %v", err)
	}

	assertMotionPhotoTags(t, still)

	// The last len(fakeVideo) bytes of the file must be the video (no padding
	// for JPEG), which is how a reader locates the trailer.
	assertTrailerEquals(t, still, fakeVideo, 0)
}

// TestMuxMotionPhoto_HEIC verifies the HEIC path wraps the video in an 8-byte
// "mpvd" box and records Padding=8.
func TestMuxMotionPhoto_HEIC(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed; skipping mux test")
	}
	if err := InitializeExifTool(); err != nil {
		t.Fatalf("init exiftool: %v", err)
	}
	defer CloseExifTool()

	// A synthetic HEIC can't survive exiftool's media-data validation on
	// rewrite, so we copy a real HEIC sample from the system. Skip where none
	// is available (e.g. CI without macOS asset images).
	sample := findSampleHEIC()
	if sample == "" {
		t.Skip("no sample HEIC available on this system; skipping HEIC mux test")
	}

	dir := t.TempDir()
	still := filepath.Join(dir, "live.heic")
	src, err := os.ReadFile(sample)
	if err != nil {
		t.Fatalf("read sample HEIC: %v", err)
	}
	if err := writeBytes(still, src); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "live.mov")
	if err := writeBytes(video, fakeVideo); err != nil {
		t.Fatal(err)
	}

	if err := MuxMotionPhoto(still, video); err != nil {
		t.Fatalf("MuxMotionPhoto: %v", err)
	}

	assertMotionPhotoTags(t, still)

	// The trailer is an mpvd box: [4-byte size][\"mpvd\"][video]. Verify the box
	// header then the video bytes sit at the end of the file.
	data, err := os.ReadFile(still)
	if err != nil {
		t.Fatal(err)
	}
	wantBox := len(fakeVideo) + heicMpvdHeaderSize
	if len(data) < wantBox {
		t.Fatalf("file too short for mpvd trailer")
	}
	trailer := data[len(data)-wantBox:]
	gotSize := binary.BigEndian.Uint32(trailer[0:4])
	if int(gotSize) != wantBox {
		t.Errorf("mpvd box size = %d, want %d", gotSize, wantBox)
	}
	if string(trailer[4:8]) != "mpvd" {
		t.Errorf("box type = %q, want mpvd", trailer[4:8])
	}
	if !bytes.Equal(trailer[8:], fakeVideo) {
		t.Errorf("video bytes inside mpvd box do not match source")
	}
}

func assertMotionPhotoTags(t *testing.T, still string) {
	t.Helper()
	out, err := exec.Command("exiftool", "-s3",
		"-XMP-GCamera:MotionPhoto",
		"-XMP-GCamera:MotionPhotoVersion",
		"-XMP-GContainer:DirectoryItemSemantic",
		"-XMP-GContainer:DirectoryItemLength",
		still,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("exiftool read: %v\n%s", err, out)
	}
	got := string(out)
	t.Logf("motion photo tags:\n%s", got)
	for _, want := range []string{"1", "Primary", "MotionPhoto"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in motion photo tags, got:\n%s", want, got)
		}
	}
}

// assertTrailerEquals checks the final len(video) bytes after skipping the
// given padding equal the expected video bytes.
func assertTrailerEquals(t *testing.T, file string, video []byte, padding int) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < len(video)+padding {
		t.Fatalf("file shorter than trailer")
	}
	trailer := data[len(data)-len(video):]
	if !bytes.Equal(trailer, video) {
		t.Errorf("trailer bytes do not match source video")
	}
}

// findSampleHEIC returns the path to a real HEIC file on the host, or "" if
// none is found. We can't embed a HEIC fixture (a synthetic one fails
// exiftool's media-data validation, and shipping a real one is heavy and
// licensing-laden), so the HEIC mux test borrows a system sample where one
// exists and skips otherwise.
func findSampleHEIC() string {
	roots := []string{
		"/System/Library/Desktop Pictures",
		"/System/Library/AssetsV2",
	}
	var found string
	for _, root := range roots {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(p), ".heic") {
				found = p
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	return found
}

package fixer

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
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

	got, err := MuxMotionPhoto(still, video)
	if err != nil {
		t.Fatalf("MuxMotionPhoto: %v", err)
	}
	if got != still {
		t.Fatalf("expected path unchanged for a correctly-named JPEG, got %s", got)
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

	got, err := MuxMotionPhoto(still, video)
	if err != nil {
		t.Fatalf("MuxMotionPhoto: %v", err)
	}
	if got != still {
		t.Fatalf("expected path unchanged for a real HEIC, got %s", got)
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

// TestMuxMotionPhoto_JPEGNamedHEIC guards the exact failure the user hit:
// Google Takeout hands back JPEG bytes carrying a .HEIC extension. The muxer
// must detect the real container from magic bytes and mux it as a JPEG (bare
// trailer append, writable XMP) rather than trusting the extension — which
// produced a file with a trailer but no readable motion metadata.
func TestMuxMotionPhoto_JPEGNamedHEIC(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed; skipping mux test")
	}
	if err := InitializeExifTool(); err != nil {
		t.Fatalf("init exiftool: %v", err)
	}
	defer CloseExifTool()

	dir := t.TempDir()
	// JPEG content, but a .heic name — the Takeout mislabeling case.
	still := filepath.Join(dir, "live.heic")
	video := filepath.Join(dir, "live.mov")
	if err := writeBytes(still, tinyJPEG); err != nil {
		t.Fatal(err)
	}
	if err := writeBytes(video, fakeVideo); err != nil {
		t.Fatal(err)
	}

	got, err := MuxMotionPhoto(still, video)
	if err != nil {
		t.Fatalf("MuxMotionPhoto on JPEG-named-.heic: %v", err)
	}

	// The extension must have been corrected to .jpg (the true container), so
	// exiftool could write and the filename no longer lies.
	wantPath := filepath.Join(dir, "live.jpg")
	if got != wantPath {
		t.Fatalf("expected extension corrected to %s, got %s", wantPath, got)
	}
	if _, err := os.Stat(still); !os.IsNotExist(err) {
		t.Errorf("original .heic-named file should have been renamed away")
	}

	// XMP must have been written (it would have errored on a real HEIC write),
	// and the trailer must be a bare append (no 8-byte mpvd box) since the file
	// is really a JPEG.
	assertMotionPhotoTags(t, got)
	assertTrailerEquals(t, got, fakeVideo, 0)
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
	assertVideoItemHasLength(t, still)
}

// assertVideoItemHasLength reads the structured GContainer directory and checks
// that the non-zero trailer Length lands on the MotionPhoto (video) item, not
// the Primary. Google Photos reads the trailer length from the video item; if
// the flattened lists misalign, the length attaches to Primary and Google
// uploads a motionless still. This is the exact regression we are guarding.
func assertVideoItemHasLength(t *testing.T, still string) {
	t.Helper()
	out, err := exec.Command("exiftool", "-j", "-struct",
		"-XMP-GContainer:all", still,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("exiftool struct read: %v\n%s", err, out)
	}
	var parsed []map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse exiftool json: %v\n%s", err, out)
	}
	if len(parsed) == 0 {
		t.Fatalf("no exiftool record:\n%s", out)
	}
	dir, ok := parsed[0]["ContainerDirectory"].([]any)
	if !ok {
		t.Fatalf("no structured ContainerDirectory in:\n%s", out)
	}
	var sawPrimary, sawVideoWithLength bool
	for _, raw := range dir {
		item, _ := raw.(map[string]any)
		inner, _ := item["Item"].(map[string]any)
		if inner == nil {
			inner = item // some exiftool versions flatten the Item wrapper
		}
		semantic, _ := inner["Semantic"].(string)
		// Length comes back as a JSON number; absent means the field is unset.
		length, hasLength := inner["Length"].(float64)
		switch semantic {
		case "Primary":
			sawPrimary = true
		case "MotionPhoto":
			if hasLength && length > 0 {
				sawVideoWithLength = true
			}
		}
	}
	if !sawPrimary {
		t.Errorf("Directory missing a Primary item:\n%s", out)
	}
	if !sawVideoWithLength {
		t.Errorf("MotionPhoto item is missing a non-zero Length (trailer would be unreadable by Google):\n%s", out)
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

package dedupe

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/draw"
)

// gradientImage builds a deterministic, structured test image. A seed-driven
// gradient orientation plus scattered bright blocks gives the perceptual
// hashes real structure to fingerprint (a flat image would hash identically
// for everything). Different seeds yield perceptually distinct layouts.
func gradientImage(w, h int, seed int64) image.Image {
	rng := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Seed-derived coefficients tilt the gradient in a per-seed direction so
	// distinct seeds produce structurally different brightness layouts.
	cx := rng.Float64()*2 - 1
	cy := rng.Float64()*2 - 1
	cxy := rng.Float64()*2 - 1
	// Scattered bright blocks add high-frequency distinctiveness.
	blocks := make([][4]int, 8)
	for i := range blocks {
		bx := rng.Intn(w - w/6)
		by := rng.Intn(h - h/6)
		blocks[i] = [4]int{bx, by, bx + w/6, by + h/6}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx := float64(x) / float64(w)
			fy := float64(y) / float64(h)
			v := cx*fx + cy*fy + cxy*fx*fy
			base := uint8((v*0.5 + 0.5) * 255)
			r, g, b := base, uint8(255-int(base)), uint8((int(base)*2)%256)
			for _, bl := range blocks {
				if x >= bl[0] && x < bl[2] && y >= bl[1] && y < bl[3] {
					r, g, b = 240, 240, 240
				}
			}
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	return img
}

// brighten returns a copy with every channel shifted up — a mild edit that
// should stay within the near-duplicate threshold.
func brighten(src image.Image, delta int) image.Image {
	b := src.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := src.At(x, y).RGBA()
			clamp := func(v uint32) uint8 {
				n := int(v>>8) + delta
				if n > 255 {
					n = 255
				}
				if n < 0 {
					n = 0
				}
				return uint8(n)
			}
			out.Set(x, y, color.RGBA{clamp(r), clamp(g), clamp(bl), 255})
		}
	}
	return out
}

// resize scales src to a new size, mimicking a different-resolution export.
func resize(src image.Image, w, h int) image.Image {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), draw.Over, nil)
	return out
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func writeJPEG(t *testing.T, path string, img image.Image, quality int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
}

func writeRaw(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// groupContaining returns the group whose files include path, or nil.
func groupContaining(groups []Group, path string) *Group {
	for i := range groups {
		for _, f := range groups[i].Files {
			if f.Path == path {
				return &groups[i]
			}
		}
	}
	return nil
}

func sameGroup(groups []Group, a, b string) bool {
	g := groupContaining(groups, a)
	if g == nil {
		return false
	}
	for _, f := range g.Files {
		if f.Path == b {
			return true
		}
	}
	return false
}

func TestHammingDistance(t *testing.T) {
	cases := []struct {
		a, b PerceptualHash
		want int
	}{
		{0, 0, 0},
		{0, 1, 1},
		{0xFFFFFFFFFFFFFFFF, 0, 64},
		{0b1010, 0b0101, 4},
	}
	for _, c := range cases {
		if got := c.a.HammingDistance(c.b); got != c.want {
			t.Errorf("HammingDistance(%x,%x)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDifferenceHash_SameImageStable(t *testing.T) {
	img := gradientImage(256, 256, 1)
	h1 := DifferenceHash(img)
	h2 := DifferenceHash(img)
	if h1 != h2 {
		t.Fatalf("dHash not deterministic: %x vs %x", h1, h2)
	}
}

func TestDifferenceHash_DistinctImagesDiffer(t *testing.T) {
	a := DifferenceHash(gradientImage(256, 256, 1))
	b := DifferenceHash(gradientImage(256, 256, 99))
	if d := a.HammingDistance(b); d < SensitivityLoose.Threshold {
		t.Fatalf("distinct images too close: distance %d", d)
	}
}

// TestScanAndGroup_FormatConversion: the same photo saved as PNG and as JPEG
// should land in one perceptual group.
func TestScanAndGroup_FormatConversion(t *testing.T) {
	dir := t.TempDir()
	base := gradientImage(300, 300, 7)

	asPNG := filepath.Join(dir, "photo.png")
	asJPEG := filepath.Join(dir, "photo.jpg")
	writePNG(t, asPNG, base)
	writeJPEG(t, asJPEG, base, 90)

	photos, err := ScanDirectory(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := FindGroups(photos, SensitivityNear)
	if !sameGroup(groups, asPNG, asJPEG) {
		t.Fatalf("PNG and JPEG of same photo did not group together; groups=%+v", groups)
	}
}

// TestScanAndGroup_Recompressed: a heavily-recompressed JPEG should group with
// its high-quality sibling.
func TestScanAndGroup_Recompressed(t *testing.T) {
	dir := t.TempDir()
	base := gradientImage(300, 300, 11)

	hi := filepath.Join(dir, "hi.jpg")
	lo := filepath.Join(dir, "lo.jpg")
	writeJPEG(t, hi, base, 95)
	writeJPEG(t, lo, base, 35)

	photos, err := ScanDirectory(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := FindGroups(photos, SensitivityNear)
	if !sameGroup(groups, hi, lo) {
		t.Fatalf("recompressed JPEGs did not group together; groups=%+v", groups)
	}
}

// TestScanAndGroup_Resized: same photo at two resolutions should group.
func TestScanAndGroup_Resized(t *testing.T) {
	dir := t.TempDir()
	base := gradientImage(400, 400, 13)

	full := filepath.Join(dir, "full.png")
	small := filepath.Join(dir, "small.png")
	writePNG(t, full, base)
	writePNG(t, small, resize(base, 160, 160))

	photos, err := ScanDirectory(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := FindGroups(photos, SensitivityNear)
	if !sameGroup(groups, full, small) {
		t.Fatalf("resized variants did not group together; groups=%+v", groups)
	}
}

// TestScanAndGroup_MildEdit: a brightness-shifted copy should group at the
// near/loose threshold.
func TestScanAndGroup_MildEdit(t *testing.T) {
	dir := t.TempDir()
	base := gradientImage(300, 300, 17)

	orig := filepath.Join(dir, "orig.png")
	edited := filepath.Join(dir, "edited.png")
	writePNG(t, orig, base)
	writePNG(t, edited, brighten(base, 25))

	photos, err := ScanDirectory(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := FindGroups(photos, SensitivityLoose)
	if !sameGroup(groups, orig, edited) {
		t.Fatalf("mild brightness edit did not group together; groups=%+v", groups)
	}
}

// TestScanAndGroup_DistinctNotGrouped: two unrelated photos must not group.
func TestScanAndGroup_DistinctNotGrouped(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.png")
	two := filepath.Join(dir, "two.png")
	writePNG(t, one, gradientImage(300, 300, 1))
	writePNG(t, two, gradientImage(300, 300, 5000))

	photos, err := ScanDirectory(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := FindGroups(photos, SensitivityNear)
	if sameGroup(groups, one, two) {
		t.Fatalf("distinct photos were grouped together; groups=%+v", groups)
	}
}

// TestScanAndGroup_ExactByteGroup: byte-identical files form a MatchExact
// group, even when undecodable.
func TestScanAndGroup_ExactByteGroup(t *testing.T) {
	dir := t.TempDir()
	base := gradientImage(200, 200, 23)
	var buf bytes.Buffer
	if err := png.Encode(&buf, base); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "copy.png")
	writeRaw(t, a, raw)
	writeRaw(t, b, raw)

	photos, err := ScanDirectory(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := FindGroups(photos, SensitivityIdentical)
	g := groupContaining(groups, a)
	if g == nil {
		t.Fatal("identical files not grouped")
	}
	if g.Kind != MatchExact {
		t.Fatalf("byte-identical group should be MatchExact, got %v", g.Kind)
	}
	if !sameGroup(groups, a, b) {
		t.Fatal("byte-identical files not in same group")
	}
}

// TestFindGroups_ExactBeatsPerceptual: a byte-identical pair should be reported
// as MatchExact, not lumped into a perceptual group.
func TestFindGroups_ExactBeatsPerceptual(t *testing.T) {
	dir := t.TempDir()
	base := gradientImage(250, 250, 29)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, base, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	x := filepath.Join(dir, "x.jpg")
	y := filepath.Join(dir, "y.jpg")
	writeRaw(t, x, raw)
	writeRaw(t, y, raw)

	photos, err := ScanDirectory(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := FindGroups(photos, SensitivityNear)
	g := groupContaining(groups, x)
	if g == nil || g.Kind != MatchExact {
		t.Fatalf("expected MatchExact group for identical bytes, got %+v", g)
	}
	if g.MaxDistance != 0 {
		t.Fatalf("exact group MaxDistance should be 0, got %d", g.MaxDistance)
	}
}

func TestScanDirectory_Progress(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		writePNG(t, filepath.Join(dir, string(rune('a'+i))+".png"), gradientImage(64, 64, int64(i)))
	}
	ch := make(chan ScanProgress, 16)
	photos, err := ScanDirectory(context.Background(), dir, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 4 {
		t.Fatalf("expected 4 photos, got %d", len(photos))
	}
	var last ScanProgress
	for p := range ch {
		last = p
	}
	if last.Total != 4 {
		t.Fatalf("expected total 4 in progress, got %d", last.Total)
	}
}

// TestFindGroups_DualHashGate locks in the AND gate between dHash and aHash:
// a close dHash alone must NOT group two photos when their aHash (brightness
// profile) is far apart, but close-on-both must group. This is the guard
// against dHash over-grouping low-detail images (skies/gradients).
func TestFindGroups_DualHashGate(t *testing.T) {
	// nBits returns a hash with the low n bits set, so HammingDistance from 0
	// is exactly n — a precise way to dial in a Hamming distance.
	nBits := func(n int) PerceptualHash {
		var h PerceptualHash
		for i := 0; i < n; i++ {
			h |= 1 << uint(i)
		}
		return h
	}

	sens := SensitivityNear // Threshold 6 (dHash), AHashMax 10 (aHash)

	// Distinct FileHashes so the two photos are never byte-identical — that
	// would short-circuit into an exact group before perceptual gating runs.
	fhA := FileHash{1}
	fhB := FileHash{2}

	t.Run("close dHash but far aHash does not group", func(t *testing.T) {
		photos := []PhotoInfo{
			{Path: "a", Decoded: true, FileHash: fhA, DHash: 0, AHash: 0},
			// dHash distance 4 (<=6 ok) but aHash distance 16 (>10) -> reject.
			{Path: "b", Decoded: true, FileHash: fhB, DHash: nBits(4), AHash: nBits(16)},
		}
		groups := FindGroups(photos, sens)
		if len(groups) != 0 {
			t.Fatalf("expected no group when aHash is far apart, got %d groups", len(groups))
		}
	})

	t.Run("close on both hashes groups", func(t *testing.T) {
		photos := []PhotoInfo{
			{Path: "a", Decoded: true, FileHash: fhA, DHash: 0, AHash: 0},
			// dHash distance 4 (<=6) and aHash distance 8 (<=10) -> group.
			{Path: "b", Decoded: true, FileHash: fhB, DHash: nBits(4), AHash: nBits(8)},
		}
		groups := FindGroups(photos, sens)
		if len(groups) != 1 || len(groups[0].Files) != 2 {
			t.Fatalf("expected one group of 2 when both hashes are close, got %v", groups)
		}
	})
}

// TestKeepCandidate verifies the auto-delete keep policy: the shortest path is
// kept, ties broken alphabetically, and every other member is returned to trash.
func TestKeepCandidate(t *testing.T) {
	t.Run("keeps shortest path", func(t *testing.T) {
		g := Group{Kind: MatchExact, Files: []PhotoInfo{
			{Path: "/photos/albums/Trip/IMG_0001.jpg"},
			{Path: "/photos/2020/IMG_0001.jpg"}, // shortest -> keep
			{Path: "/photos/albums/Other/IMG_0001.jpg"},
		}}
		keep, trash := KeepCandidate(g)
		if keep.Path != "/photos/2020/IMG_0001.jpg" {
			t.Fatalf("kept %q, want the shortest path", keep.Path)
		}
		if len(trash) != 2 {
			t.Fatalf("expected 2 files to trash, got %d", len(trash))
		}
		for _, f := range trash {
			if f.Path == keep.Path {
				t.Fatalf("kept file %q also appears in trash list", keep.Path)
			}
		}
	})

	t.Run("breaks equal-length ties alphabetically", func(t *testing.T) {
		g := Group{Kind: MatchExact, Files: []PhotoInfo{
			{Path: "/p/b.jpg"},
			{Path: "/p/a.jpg"}, // same length, alphabetically first -> keep
		}}
		keep, trash := KeepCandidate(g)
		if keep.Path != "/p/a.jpg" {
			t.Fatalf("kept %q, want /p/a.jpg", keep.Path)
		}
		if len(trash) != 1 || trash[0].Path != "/p/b.jpg" {
			t.Fatalf("expected to trash /p/b.jpg, got %v", trash)
		}
	})
}

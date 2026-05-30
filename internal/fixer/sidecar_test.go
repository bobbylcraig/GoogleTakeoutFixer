package fixer

import (
	"os"
	"path/filepath"
	"testing"
)

// setupSidecarDir creates a temp dir containing the given media + json files
// and returns the dir. Files are created empty.
func setupSidecarDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	// Each test gets a fresh dir; clear the dir cache so reads aren't stale.
	ClearCache()
	return dir
}

func TestFindSidecar(t *testing.T) {
	cases := []struct {
		name      string
		mediaFile string
		files     []string
		wantJSON  string // empty means expect no match
	}{
		{
			name:      "older form name.json",
			mediaFile: "IMG_0001.jpg",
			files:     []string{"IMG_0001.jpg", "IMG_0001.json"},
			wantJSON:  "IMG_0001.json",
		},
		{
			name:      "name.ext.json form",
			mediaFile: "IMG_0001.jpg",
			files:     []string{"IMG_0001.jpg", "IMG_0001.jpg.json"},
			wantJSON:  "IMG_0001.jpg.json",
		},
		{
			name:      "supplementary-metadata long form",
			mediaFile: "IMG_0001.jpg",
			files:     []string{"IMG_0001.jpg", "IMG_0001.jpg.supplementary-metadata.json"},
			wantJSON:  "IMG_0001.jpg.supplementary-metadata.json",
		},
		{
			name:      "truncated supplementary-metadata",
			mediaFile: "IMG_0001.jpg",
			files:     []string{"IMG_0001.jpg", "IMG_0001.jpg.supplementa.json"},
			wantJSON:  "IMG_0001.jpg.supplementa.json",
		},
		{
			name:      "numbered duplicate counter migrates after ext",
			mediaFile: "IMG_0001(1).jpg",
			files: []string{
				"IMG_0001.jpg", "IMG_0001.jpg.json",
				"IMG_0001(1).jpg", "IMG_0001.jpg(1).json",
			},
			wantJSON: "IMG_0001.jpg(1).json",
		},
		{
			name:      "numbered duplicate with supplementary suffix",
			mediaFile: "IMG_0001(2).jpg",
			files: []string{
				"IMG_0001(2).jpg",
				"IMG_0001.jpg.supplementary-metadata(2).json",
			},
			wantJSON: "IMG_0001.jpg.supplementary-metadata(2).json",
		},
		{
			name:      "edited derivative falls back to original sidecar",
			mediaFile: "IMG_0001-edited.jpg",
			files: []string{
				"IMG_0001.jpg", "IMG_0001.jpg.supplementary-metadata.json",
				"IMG_0001-edited.jpg",
			},
			wantJSON: "IMG_0001.jpg.supplementary-metadata.json",
		},
		{
			name:      "case insensitive match",
			mediaFile: "Img_0001.JPG",
			files:     []string{"Img_0001.JPG", "img_0001.jpg.SUPPLEMENTARY-METADATA.json"},
			wantJSON:  "img_0001.jpg.SUPPLEMENTARY-METADATA.json",
		},
		{
			name:      "no sidecar present",
			mediaFile: "IMG_0009.jpg",
			files:     []string{"IMG_0009.jpg"},
			wantJSON:  "",
		},
		{
			name:      "does not match unrelated prefix-sharing file",
			mediaFile: "IMG_1.jpg",
			files: []string{
				"IMG_1.jpg",
				"IMG_12.jpg.supplementary-metadata.json", // belongs to IMG_12
			},
			wantJSON: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupSidecarDir(t, tc.files...)
			got, err := FindSidecar(filepath.Join(dir, tc.mediaFile))
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantJSON == "" {
				if got != "" {
					t.Fatalf("expected no match, got %q", got)
				}
				return
			}
			want := filepath.Join(dir, tc.wantJSON)
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

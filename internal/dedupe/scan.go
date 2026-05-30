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
	"context"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	// Register decoders for the formats we support.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// supportedImageExts are the formats whose pixels we can decode for perceptual
// hashing. (HEIC needs a cgo decoder and is handled as exact-byte only.)
var supportedImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".tiff": true, ".tif": true, ".bmp": true,
}

// PhotoInfo holds everything the engine knows about one scanned file.
type PhotoInfo struct {
	Path        string
	Size        int64
	Width       int
	Height      int
	FileHash    FileHash
	DHash       PerceptualHash
	AHash       PerceptualHash
	Decoded     bool // false when pixels could not be decoded (hashes are zero)
	DecodeError error
}

// ScanProgress reports scanning progress for a UI.
type ScanProgress struct {
	Total     int
	Processed int
	Current   string
}

// isSupportedImage reports whether a path has a decodable image extension.
func isSupportedImage(path string) bool {
	return supportedImageExts[strings.ToLower(filepath.Ext(path))]
}

// collectImagePaths walks root recursively and returns decodable image paths.
func collectImagePaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isSupportedImage(p) {
			paths = append(paths, p)
		}
		return nil
	})
	return paths, err
}

// hashOne decodes a file and fills in size, dimensions, and all hashes.
func hashOne(path string) PhotoInfo {
	info := PhotoInfo{Path: path}

	if st, err := os.Stat(path); err == nil {
		info.Size = st.Size()
	}

	if fh, err := HashFileBytes(path); err == nil {
		info.FileHash = fh
	}

	f, err := os.Open(path)
	if err != nil {
		info.DecodeError = err
		return info
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		info.DecodeError = err
		return info
	}

	b := img.Bounds()
	info.Width = b.Dx()
	info.Height = b.Dy()
	info.DHash = DifferenceHash(img)
	info.AHash = AverageHash(img)
	info.Decoded = true
	return info
}

// ScanDirectory walks root, hashing every supported image concurrently.
// Progress is delivered on progressCh if non-nil; the channel is closed when
// scanning finishes. The returned slice includes entries that failed to decode
// (Decoded=false) so the caller can still surface exact-byte duplicates.
func ScanDirectory(ctx context.Context, root string, progressCh chan<- ScanProgress) ([]PhotoInfo, error) {
	if progressCh != nil {
		defer close(progressCh)
	}

	paths, err := collectImagePaths(root)
	if err != nil {
		return nil, err
	}

	results := make([]PhotoInfo, len(paths))
	jobs := make(chan int, len(paths))
	for i := range paths {
		jobs <- i
	}
	close(jobs)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		processed int
	)
	workers := runtime.NumCPU()
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				results[i] = hashOne(paths[i])

				if progressCh != nil {
					mu.Lock()
					processed++
					p := ScanProgress{Total: len(paths), Processed: processed, Current: paths[i]}
					mu.Unlock()
					select {
					case progressCh <- p:
					default: // never block scanning on a slow UI consumer
					}
				}
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		return results, ctx.Err()
	}
	return results, nil
}

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

// Package dedupe finds duplicate and near-duplicate photos. It combines an
// exact byte hash (for identical files) with perceptual hashes (for files that
// look the same despite differing in format, compression, or light edits).
package dedupe

import (
	"crypto/sha256"
	"image"
	"io"
	"math/bits"
	"os"

	"golang.org/x/image/draw"
)

// PerceptualHash is a 64-bit fingerprint of an image's visual structure.
// Two images that look alike produce hashes a small Hamming distance apart,
// regardless of file format, resolution, or compression.
type PerceptualHash uint64

// HammingDistance returns the number of differing bits between two perceptual
// hashes. 0 means visually identical; larger means more different.
func (h PerceptualHash) HammingDistance(other PerceptualHash) int {
	return bits.OnesCount64(uint64(h) ^ uint64(other))
}

// FileHash is the SHA-256 of a file's raw bytes, used to detect byte-identical
// duplicates with certainty.
type FileHash [32]byte

// HashFileBytes returns the SHA-256 of the file's contents.
func HashFileBytes(path string) (FileHash, error) {
	var out FileHash
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return out, err
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}

// grayMatrix downscales img to size x size and returns row-major grayscale
// values in [0,255]. Downscaling first makes the perceptual hashes robust to
// resolution and compression differences.
func grayMatrix(img image.Image, size int) []float64 {
	small := image.NewGray(image.Rect(0, 0, size, size))
	// CatmullRom gives smooth, detail-preserving downsampling.
	draw.CatmullRom.Scale(small, small.Bounds(), img, img.Bounds(), draw.Over, nil)

	out := make([]float64, size*size)
	for i, v := range small.Pix {
		out[i] = float64(v)
	}
	return out
}

// DifferenceHash computes a dHash: for a 9x8 grayscale grid, each bit records
// whether a pixel is brighter than the one to its right. Robust to scaling,
// format changes, and recompression; tolerant of mild brightness/color shifts.
func DifferenceHash(img image.Image) PerceptualHash {
	const w, h = 9, 8
	small := image.NewGray(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(small, small.Bounds(), img, img.Bounds(), draw.Over, nil)

	var hash PerceptualHash
	bit := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w-1; x++ {
			left := small.GrayAt(x, y).Y
			right := small.GrayAt(x+1, y).Y
			if left > right {
				hash |= 1 << uint(bit)
			}
			bit++
		}
	}
	return hash
}

// AverageHash computes an aHash: each bit records whether a pixel in an 8x8
// grayscale grid is above the grid's mean brightness. Cheap and a useful
// second opinion alongside dHash.
func AverageHash(img image.Image) PerceptualHash {
	const size = 8
	px := grayMatrix(img, size)

	var sum float64
	for _, v := range px {
		sum += v
	}
	mean := sum / float64(len(px))

	var hash PerceptualHash
	for i, v := range px {
		if v >= mean {
			hash |= 1 << uint(i)
		}
	}
	return hash
}

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

package fixer

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Google Motion Photo v1 (https://developer.android.com/media/platform/motion-photo-format)
// stores a still image with the video appended as a trailer. The image's XMP
// carries a GContainer directory describing the trailer so readers (Google
// Photos, etc.) can locate and play the video. This makes a single file behave
// as a motion photo, the way an iPhone Live Photo does after import.

// heicMpvdHeaderSize is the size of the ISO-BMFF "mpvd" box header (4-byte
// big-endian size + 4-byte type) that wraps the video in a HEIC motion photo.
// The spec requires the primary item Padding to equal this for HEIC/AVIF.
const heicMpvdHeaderSize = 8

// IsMotionPhotoStill reports whether a still-image extension is a supported
// Motion Photo primary container (JPEG or HEIC; AVIF is spec-allowed but not
// emitted by Takeout).
func IsMotionPhotoStill(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".heic", ".heif":
		return true
	}
	return false
}

// stillContainer is the real on-disk container of a still, detected from magic
// bytes rather than the file extension. Google Takeout frequently hands back a
// JPEG carrying a .HEIC extension (the server transcodes but keeps the name);
// trusting the extension would mux it with the wrong trailer format and write
// XMP exiftool rejects, silently producing a motionless file.
type stillContainer int

const (
	stillJPEG stillContainer = iota
	stillHEIC
	stillUnknown
)

// detectStillContainer reads the leading bytes of the still to determine its
// true container. JPEG begins with FF D8 FF. HEIC/HEIF is ISO-BMFF: a "ftyp"
// box at offset 4 whose major/compatible brand is in the HEIF family.
func detectStillContainer(stillPath string) (stillContainer, error) {
	f, err := os.Open(stillPath)
	if err != nil {
		return stillUnknown, err
	}
	defer f.Close()

	var hdr [32]byte
	n, err := io.ReadFull(f, hdr[:])
	// ReadFull on a short file returns ErrUnexpectedEOF with the partial bytes;
	// that's fine — we only need the first dozen or so.
	if err != nil && err != io.ErrUnexpectedEOF {
		return stillUnknown, err
	}
	b := hdr[:n]

	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return stillJPEG, nil
	}
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		// Major brand at [8:12], plus any compatible brands that follow; treat
		// the whole header window as the brand set we scan.
		brand := string(b[8:12])
		switch brand {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "heif", "mif1", "msf1", "avif":
			return stillHEIC, nil
		}
		// Some HEICs carry a non-HEIF major brand but list one as compatible;
		// scan the remaining header bytes for a HEIF brand.
		for _, fam := range []string{"heic", "heix", "heif", "mif1", "avif"} {
			if strings.Contains(string(b), fam) {
				return stillHEIC, nil
			}
		}
	}
	return stillUnknown, nil
}

// motionPhotoMime maps a detected container to its Motion Photo primary MIME.
func motionPhotoMime(c stillContainer) string {
	if c == stillHEIC {
		return "image/heic"
	}
	return "image/jpeg"
}

// correctExtFor returns the canonical extension for a detected container.
func correctExtFor(c stillContainer) string {
	if c == stillHEIC {
		return ".heic"
	}
	return ".jpg"
}

// MuxMotionPhoto turns an already-written still file at stillPath into a Google
// Motion Photo by embedding the video at videoPath as a trailer and writing the
// required GCamera/GContainer XMP. It returns the still's final path, which may
// differ from stillPath when the extension is corrected (see below).
//
// The still's true container is detected from magic bytes, because Google
// Takeout frequently hands back JPEG content under a .HEIC name. exiftool keys
// its writer off the *extension*, so it refuses to write XMP into a JPEG named
// .HEIC. When the extension disagrees with the content we rename the file to
// its true extension before writing — this both unblocks exiftool and leaves an
// honest filename, in keeping with the tool's purpose of cleaning up Takeout.
//
// For JPEG the video bytes are appended directly. For HEIC the video is wrapped
// in a top-level "mpvd" box (Padding=8) so the file stays a valid ISO-BMFF
// container. The GContainer Item:Length is the full trailer length (video plus,
// for HEIC, the 8-byte box header).
func MuxMotionPhoto(stillPath, videoPath string) (string, error) {
	vinfo, err := os.Stat(videoPath)
	if err != nil {
		return stillPath, fmt.Errorf("stat video: %w", err)
	}
	videoSize := vinfo.Size()

	container, err := detectStillContainer(stillPath)
	if err != nil {
		return stillPath, fmt.Errorf("detect still container: %w", err)
	}
	if container == stillUnknown {
		return stillPath, fmt.Errorf("unrecognized still container for %s; refusing to mux", filepath.Base(stillPath))
	}
	heic := container == stillHEIC

	// Correct a lying extension before writing, so exiftool will accept the
	// file and the output name matches the real content.
	if !strings.EqualFold(filepath.Ext(stillPath), correctExtFor(container)) {
		corrected := strings.TrimSuffix(stillPath, filepath.Ext(stillPath)) + correctExtFor(container)
		if corrected != stillPath {
			if _, statErr := os.Lstat(corrected); statErr == nil {
				return stillPath, fmt.Errorf("cannot correct extension: %s already exists", filepath.Base(corrected))
			}
			if err := os.Rename(stillPath, corrected); err != nil {
				return stillPath, fmt.Errorf("correcting still extension: %w", err)
			}
			stillPath = corrected
		}
	}

	trailerLen := videoSize
	padding := 0
	if heic {
		trailerLen += heicMpvdHeaderSize
		padding = heicMpvdHeaderSize
	}

	// 1) Write the Motion Photo XMP into the still. Item:Length must describe
	//    the trailer we are about to append, so it is set before appending.
	if err := writeMotionPhotoXMP(stillPath, motionPhotoMime(container), trailerLen, padding); err != nil {
		return stillPath, err
	}

	// 2) Append the trailer (the still on disk now already includes the XMP).
	if err := appendVideoTrailer(stillPath, videoPath, heic, videoSize); err != nil {
		return stillPath, err
	}
	return stillPath, nil
}

// writeMotionPhotoXMP sets the GCamera flags and the GContainer directory on
// the still via the persistent exiftool process. The directory lists the
// primary image first and the video item last, per the spec.
func writeMotionPhotoXMP(stillPath, primaryMime string, videoLength int64, padding int) error {
	// exiftool reconstructs each GContainer Item by *list position* across the
	// flattened DirectoryItem* lists. Every list must therefore carry one entry
	// per item, or a short list (e.g. a single Length) silently aligns to the
	// wrong item. We emit Length/Padding for BOTH items — 0 for the primary
	// (it spans everything before the trailer) and the real values for the
	// video item — so the trailer length lands on the MotionPhoto item where
	// Google Photos reads it.
	args := []string{
		"-overwrite_original",
		"-XMP-GCamera:MotionPhoto=1",
		"-XMP-GCamera:MotionPhotoVersion=1",
		"-XMP-GCamera:MotionPhotoPresentationTimestampUs=-1",
		// Clear any stale lists first. The GContainer directory is exposed by
		// exiftool under the XMP-GContainer family-1 group (not XMP-Container).
		"-XMP-GContainer:DirectoryItemMime-=",
		"-XMP-GContainer:DirectoryItemSemantic-=",
		"-XMP-GContainer:DirectoryItemLength-=",
		"-XMP-GContainer:DirectoryItemPadding-=",
		// Item 1: the primary still image (Length 0 — it precedes the trailer).
		"-XMP-GContainer:DirectoryItemMime+=" + primaryMime,
		"-XMP-GContainer:DirectoryItemSemantic+=Primary",
		"-XMP-GContainer:DirectoryItemLength+=0",
		// Item 2: the appended video trailer.
		"-XMP-GContainer:DirectoryItemMime+=video/mp4",
		"-XMP-GContainer:DirectoryItemSemantic+=MotionPhoto",
		fmt.Sprintf("-XMP-GContainer:DirectoryItemLength+=%d", videoLength),
		"-charset", "filename=utf8",
	}
	// Padding aligns the same way: a value per item. The primary needs no
	// padding (0); the HEIC video item's padding is the mpvd box header.
	args = append(args,
		"-XMP-GContainer:DirectoryItemPadding+=0",
		fmt.Sprintf("-XMP-GContainer:DirectoryItemPadding+=%d", padding),
	)
	if err := runExifAssign(args, stillPath); err != nil {
		return fmt.Errorf("writing Motion Photo XMP: %w", err)
	}
	return nil
}

// appendVideoTrailer appends the video to the still. For HEIC it first writes
// an 8-byte "mpvd" box header sized to enclose the video, then the video bytes.
func appendVideoTrailer(stillPath, videoPath string, heic bool, videoSize int64) error {
	out, err := os.OpenFile(stillPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	if heic {
		// ISO-BMFF box: 4-byte big-endian total size, then 4-byte type "mpvd".
		boxSize := videoSize + heicMpvdHeaderSize
		if boxSize > 0xFFFFFFFF {
			return fmt.Errorf("video too large for 32-bit mpvd box: %d bytes", videoSize)
		}
		var hdr [heicMpvdHeaderSize]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(boxSize))
		copy(hdr[4:8], "mpvd")
		if _, err := out.Write(hdr[:]); err != nil {
			return err
		}
	}

	in, err := os.Open(videoPath)
	if err != nil {
		return err
	}
	defer in.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("appending video trailer: %w", err)
	}
	return nil
}

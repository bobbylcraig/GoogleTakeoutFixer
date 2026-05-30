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

// motionPhotoMime maps a still path to its Motion Photo primary MIME type.
func motionPhotoMime(stillPath string) string {
	switch strings.ToLower(filepath.Ext(stillPath)) {
	case ".heic", ".heif":
		return "image/heic"
	default:
		return "image/jpeg"
	}
}

// isHEIC reports whether the still is a HEIC/HEIF container (which requires the
// mpvd-box trailer form rather than a bare append).
func isHEIC(stillPath string) bool {
	ext := strings.ToLower(filepath.Ext(stillPath))
	return ext == ".heic" || ext == ".heif"
}

// MuxMotionPhoto turns an already-written still file at stillPath into a Google
// Motion Photo by embedding the video at videoPath as a trailer and writing the
// required GCamera/GContainer XMP. The still is modified in place.
//
// For JPEG the video bytes are appended directly. For HEIC the video is wrapped
// in a top-level "mpvd" box (Padding=8) so the file stays a valid ISO-BMFF
// container. The GContainer Item:Length is the full trailer length (video plus,
// for HEIC, the 8-byte box header).
func MuxMotionPhoto(stillPath, videoPath string) error {
	vinfo, err := os.Stat(videoPath)
	if err != nil {
		return fmt.Errorf("stat video: %w", err)
	}
	videoSize := vinfo.Size()

	heic := isHEIC(stillPath)
	trailerLen := videoSize
	padding := 0
	if heic {
		trailerLen += heicMpvdHeaderSize
		padding = heicMpvdHeaderSize
	}

	// 1) Write the Motion Photo XMP into the still. Item:Length must describe
	//    the trailer we are about to append, so it is set before appending.
	if err := writeMotionPhotoXMP(stillPath, motionPhotoMime(stillPath), trailerLen, padding); err != nil {
		return err
	}

	// 2) Append the trailer (the still on disk now already includes the XMP).
	return appendVideoTrailer(stillPath, videoPath, heic, videoSize)
}

// writeMotionPhotoXMP sets the GCamera flags and the GContainer directory on
// the still via the persistent exiftool process. The directory lists the
// primary image first and the video item last, per the spec.
func writeMotionPhotoXMP(stillPath, primaryMime string, videoLength int64, padding int) error {
	args := []string{
		"-overwrite_original",
		"-XMP-GCamera:MotionPhoto=1",
		"-XMP-GCamera:MotionPhotoVersion=1",
		"-XMP-GCamera:MotionPhotoPresentationTimestampUs=-1",
		// Directory item 1: the primary still image. The GContainer directory is
		// exposed by exiftool under the XMP-GContainer family-1 group (not
		// XMP-Container), with flattened DirectoryItem* tags.
		"-XMP-GContainer:DirectoryItemMime-=", // clear any stale list first
		"-XMP-GContainer:DirectoryItemSemantic-=",
		"-XMP-GContainer:DirectoryItemMime+=" + primaryMime,
		"-XMP-GContainer:DirectoryItemSemantic+=Primary",
		// Directory item 2: the appended video trailer.
		"-XMP-GContainer:DirectoryItemMime+=video/mp4",
		"-XMP-GContainer:DirectoryItemSemantic+=MotionPhoto",
		fmt.Sprintf("-XMP-GContainer:DirectoryItemLength+=%d", videoLength),
		"-charset", "filename=utf8",
	}
	if padding > 0 {
		args = append(args, fmt.Sprintf("-XMP-GContainer:DirectoryItemPadding+=%d", padding))
	}
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

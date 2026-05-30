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
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache for diectory entries to prevent excessive disk reads (issue #5)
var (
	dirCache     = make(map[string][]os.DirEntry)
	dirCacheLock sync.RWMutex
)

// ReadDirCached returns cached directories or reads them it not present
func ReadDirCached(dir string) ([]os.DirEntry, error) {
	dirCacheLock.RLock()
	entries, ok := dirCache[dir]
	dirCacheLock.RUnlock()

	// Cache hit, return entries
	if ok {
		return entries, nil
	}

	dirCacheLock.Lock()
	defer dirCacheLock.Unlock()

	// Check again in case it was created while waiting for lock
	if entries, ok = dirCache[dir]; ok {
		return entries, nil
	}

	// Read directory and cache results
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	dirCache[dir] = entries

	return entries, nil
}

// ClearCache clears the directory cache for all paths
func ClearCache() {
	dirCacheLock.Lock()
	defer dirCacheLock.Unlock()
	// Reallocate map to clear everything
	dirCache = make(map[string][]os.DirEntry)
}

// ClearCacheDir clears the directory cache for a specific path
func ClearCacheDir(dir string) {
	dirCacheLock.Lock()
	defer dirCacheLock.Unlock()
	delete(dirCache, dir)
}

// All media extension to differ between media files and other files
var imageExtensions = []string{".jpg", ".jpeg", ".png", ".heic", ".gif", ".webp", ".tiff", ".tif", ".bmp"}

var videoExtensions = []string{".mp4", ".mov", ".avi", ".mkv", ".m4v", ".3gp"}

// Checks whether a file is a video file based on its extension
func IsVideoFile(path string) bool {
	extension := filepath.Ext(path)
	return slices.Contains(videoExtensions, strings.ToLower(extension))
}

// hashFile returns the SHA-256 of a file's contents.
func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// sameContent reports whether two files have identical contents.
// It short-circuits on differing size before hashing.
func sameContent(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if ai.Size() != bi.Size() {
		return false, nil
	}

	ah, err := hashFile(a)
	if err != nil {
		return false, err
	}
	bh, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return string(ah) == string(bh), nil
}

// ResolveDestPath decides where a source file should be written given that
// destPath may already be taken. It returns:
//   - skip=true when an existing file at destPath (or one of its numbered
//     variants) is byte-identical to the source, meaning nothing needs writing.
//   - otherwise the first available path, appending " (n)" before the
//     extension so distinct files sharing a name are kept side by side.
func ResolveDestPath(sourcePath, destPath string) (resolved string, skip bool, err error) {
	ext := filepath.Ext(destPath)
	base := strings.TrimSuffix(destPath, ext)

	for n := 0; ; n++ {
		candidate := destPath
		if n > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, n, ext)
		}

		info, statErr := os.Stat(candidate)
		if os.IsNotExist(statErr) {
			return candidate, false, nil
		}
		if statErr != nil {
			return "", false, statErr
		}
		if info.IsDir() {
			continue
		}

		identical, err := sameContent(sourcePath, candidate)
		if err != nil {
			return "", false, err
		}
		if identical {
			return candidate, true, nil
		}
	}
}

// ResolvePairDestPaths resolves output paths for a Live Photo pair so both
// halves receive the *same* " (n)" suffix, keeping their base names matched.
//
// It scans for the lowest n (starting at 0 = no suffix) where neither the image
// nor the video destination collides with a *different* file. A slot is usable
// when each side is either free or byte-identical to its source. The returned
// skips report whether each side is already present byte-identically (so it
// need not be rewritten); when both are skipped the caller can skip the pair.
func ResolvePairDestPaths(imageSrc, videoSrc, imageDest, videoDest string) (
	imageOut, videoOut string, skipImage, skipVideo bool, err error,
) {
	imgExt := filepath.Ext(imageDest)
	imgBase := strings.TrimSuffix(imageDest, imgExt)
	vidExt := filepath.Ext(videoDest)
	vidBase := strings.TrimSuffix(videoDest, vidExt)

	for n := 0; ; n++ {
		imgCand, vidCand := imageDest, videoDest
		if n > 0 {
			imgCand = fmt.Sprintf("%s (%d)%s", imgBase, n, imgExt)
			vidCand = fmt.Sprintf("%s (%d)%s", vidBase, n, vidExt)
		}

		imgState, err := slotState(imageSrc, imgCand)
		if err != nil {
			return "", "", false, false, err
		}
		vidState, err := slotState(videoSrc, vidCand)
		if err != nil {
			return "", "", false, false, err
		}

		// Both names must be usable at this same n for the pair to stay linked.
		if imgState == slotTaken || vidState == slotTaken {
			continue
		}
		return imgCand, vidCand, imgState == slotIdentical, vidState == slotIdentical, nil
	}
}

type slotResult int

const (
	slotFree      slotResult = iota // nothing at this path
	slotIdentical                   // a byte-identical copy already exists
	slotTaken                       // a different file occupies this path
)

// slotState classifies whether dest is free, already holds the same bytes as
// src, or is occupied by a different file. Directories count as taken.
func slotState(src, dest string) (slotResult, error) {
	info, statErr := os.Stat(dest)
	if os.IsNotExist(statErr) {
		return slotFree, nil
	}
	if statErr != nil {
		return slotFree, statErr
	}
	if info.IsDir() {
		return slotTaken, nil
	}
	identical, err := sameContent(src, dest)
	if err != nil {
		return slotFree, err
	}
	if identical {
		return slotIdentical, nil
	}
	return slotTaken, nil
}

// Duplicate a file from one path to another. On copy failure the partial (or
// reserved placeholder) output file is removed so a re-run is not misled by a
// truncated leftover.
func DuplicateFile(inputPath string, outputPath string) error {
	sourceFile, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		destFile.Close()
		os.Remove(outputPath)
		return err
	}
	return destFile.Close()
}

// Discover directories within a path non recursively
func DiscoverDirs(path string) ([]os.DirEntry, error) {
	var dirList []os.DirEntry

	files, err := os.ReadDir(path)

	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if file.IsDir() {
			dirList = append(dirList, file)
		}
	}

	return dirList, nil
}

// editedSuffixes are markers Google adds to derivative files that have no
// sidecar of their own; the sidecar belongs to the original media file.
// Localized variants exist; these cover the common cases.
var editedSuffixes = []string{"-edited", "-bearbeitet", "-modifié", "-ha editado", "-bewerkt", "-edytowane"}

// dupNumberRe matches a Google duplicate counter like "(1)" at the end of a
// base name, e.g. "IMG_0001(1)".
var dupNumberRe = regexp.MustCompile(`^(.*)\((\d+)\)$`)

// leadingDupRe matches a duplicate counter at the start of a string, e.g.
// "(1).supplementary-metadata" -> captures "(1)".
var leadingDupRe = regexp.MustCompile(`^\(\d+\)`)

// stripEditedSuffix removes a trailing "-edited" style marker from a base name
// (the part without extension). Returns the stripped base and whether one was found.
func stripEditedSuffix(base string) (string, bool) {
	lower := strings.ToLower(base)
	for _, suf := range editedSuffixes {
		if strings.HasSuffix(lower, suf) {
			return base[:len(base)-len(suf)], true
		}
	}
	return base, false
}

// sidecarCandidates returns, in priority order, the exact JSON sidecar
// filenames Google Takeout may have used for a given media filename.
//
// Google's naming is irregular. For a media file "name.ext" the sidecar may be:
//   - name.ext.supplementary-metadata.json   (current, long form)
//   - name.ext.suppl.json / ...supplementa.json (truncated long form)
//   - name.ext.json                           (older form)
//   - name.json                               (oldest form)
//
// Duplicate counters migrate outside the extension: "name(1).ext" pairs with
// "name.ext(1).json" (and also "name.ext.json"-style variants carrying "(n)").
// "-edited" derivatives carry no sidecar and resolve to the original's.
func sidecarCandidates(fileName string) []string {
	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)

	// Detect a duplicate counter on the base, e.g. IMG_0001(1) -> base IMG_0001, n "(1)".
	dupSuffix := ""
	if m := dupNumberRe.FindStringSubmatch(base); m != nil {
		base = m[1]
		dupSuffix = "(" + m[2] + ")"
	}

	// Reconstruct the "stem" Google uses as the sidecar prefix: original
	// filename without the duplicate counter, e.g. "IMG_0001.jpg".
	stem := base + ext

	var candidates []string
	// Long form and its variants, with the duplicate counter placed after .json's stem.
	candidates = append(candidates,
		stem+".supplementary-metadata"+dupSuffix+".json",
		stem+dupSuffix+".json",
		base+dupSuffix+".json",
	)
	return candidates
}

// Find a matching sidecar JSON for a media file.
func FindSidecar(imagePath string) (string, error) {
	dir := filepath.Dir(imagePath)
	fileName := filepath.Base(imagePath)

	entries, err := ReadDirCached(dir)
	if err != nil {
		return "", err
	}

	// Build a case-insensitive lookup of JSON files in the directory.
	jsonByLower := make(map[string]string, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".json") {
			jsonByLower[strings.ToLower(name)] = name
		}
	}
	if len(jsonByLower) == 0 {
		return "", nil
	}

	// "-edited" derivatives use the original media file's sidecar, so resolve
	// against the original name first, then fall back to the edited name.
	namesToTry := []string{fileName}
	if ext := filepath.Ext(fileName); ext != "" {
		stem := strings.TrimSuffix(fileName, ext)
		if stripped, ok := stripEditedSuffix(stem); ok {
			namesToTry = append([]string{stripped + ext}, namesToTry...)
		}
	}

	for _, name := range namesToTry {
		// 1) Exact candidate matches (handles long form, truncation-free cases,
		//    older forms, and the (n) duplicate counter).
		for _, cand := range sidecarCandidates(name) {
			if actual, ok := jsonByLower[strings.ToLower(cand)]; ok {
				return filepath.Join(dir, actual), nil
			}
		}

		// 2) Truncation fallback: Google truncates long sidecar names, so
		//    "IMG_0001.jpg.supplementary-metadata.json" can become
		//    "IMG_0001.jpg.supplementa.json". Match a JSON whose name starts
		//    with the media filename (case-insensitive) and is a plausible
		//    supplementary-metadata fragment.
		if match := matchTruncatedSidecar(name, jsonByLower); match != "" {
			return filepath.Join(dir, match), nil
		}
	}

	return "", nil
}

// matchTruncatedSidecar finds a JSON whose name is a truncated
// "<fileName>.supplementary-metadata.json". It requires the JSON to start with
// the media filename and the remainder to be a prefix of the supplementary
// suffix, avoiding accidental matches with unrelated files that merely share a
// prefix (e.g. IMG_1 vs IMG_12).
func matchTruncatedSidecar(fileName string, jsonByLower map[string]string) string {
	lowerFile := strings.ToLower(fileName)
	const supplement = ".supplementary-metadata"

	var best string
	for lower, actual := range jsonByLower {
		body := strings.TrimSuffix(lower, ".json")
		if !strings.HasPrefix(body, lowerFile) {
			continue
		}
		remainder := body[len(lowerFile):]
		// A duplicate counter "(n)" may lead the remainder before the suffix; drop it.
		remainder = leadingDupRe.ReplaceAllString(remainder, "")
		// Valid only if what's left is empty (".json" directly) or a (possibly
		// truncated) leading fragment of ".supplementary-metadata".
		if remainder == "" || strings.HasPrefix(supplement, remainder) {
			// Prefer the longest body (closest to the real, untruncated name).
			if len(actual) > len(best) {
				best = actual
			}
		}
	}
	return best
}

// Checks if the file at the given path has the specified extension
func IsNameExtension(extension string, path string) bool {
	return strings.EqualFold(filepath.Ext(path), extension)
}

// Checks whether a directory is a standart google year folder
func IsYearFolder(dirPath string) (bool, error) {
	// Year folder prefixes of some countries
	// yearPrefixes is mostly made by AI. I have not verified these, but i assume they are primarily correct.
	// Please create an issue if you find any mistakes or if you want to add more languages.
	yearPrefixes := []string{
		"Photos from ",     // English
		"Fotos von ",       // German
		"Photos de ",       // French
		"Foto del ",        // Italian
		"Fotos de ",        // Spanish / Portuguese
		"Foto's van ",      // Dutch
		"Zdjęcia z ",       // Polish
		"Фотографии из ",   // Russian
		"Foton från ",      // Swedish
		"Bilder fra ",      // Norwegian
		"Billeder fra ",    // Danish
		"Fotoğraflar ",     // Turkish
		"Fotografie z ",    // Czech
		"Fotók a ",         // Hungarian
		"Φωτογραφίες από ", // Greek
		"Fotografii din ",  // Romanian
		"Foto dari ",       // Indonesian
		"รูปภาพจาก ",       // Thai
		"Ảnh từ ",          // Vietnamese
	}

	for _, prefix := range yearPrefixes {
		if strings.HasPrefix(dirPath, prefix) {
			// The rest of the string has to be 4 characters long
			yearPart := strings.TrimPrefix(dirPath, prefix)
			if matched, _ := regexp.MatchString(`^\d{4}$`, yearPart); matched {
				return true, nil
			}
		}
	}
	return false, nil
}

// Checks whether a file, that is provided using its path, is a media file
func IsMediaFile(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	isImage := slices.Contains(imageExtensions, extension)
	isVideo := slices.Contains(videoExtensions, extension)
	return isImage || isVideo
}

// Attempts to find an image file with the same base name as the video file
// This is used for live photos where the metadata is the images sidecar
// I think error handling could be improved here
func FindImagePartner(videoPath string) (string, error) {
	if !IsVideoFile(videoPath) {
		return "", nil
	}

	dir := filepath.Dir(videoPath)
	extension := filepath.Ext(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), extension)

	// Check all image extensions for a match
	for _, imgExt := range imageExtensions {
		candidate := filepath.Join(dir, base+imgExt)

		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		// Try uppercase extension
		candidateUpper := filepath.Join(dir, base+strings.ToUpper(imgExt))
		if _, err := os.Stat(candidateUpper); err == nil {
			return candidateUpper, nil
		}
	}

	return "", nil
}

// livePhotoImageExts are the still-image halves of an iPhone Live Photo. Older
// devices emitted JPG, newer ones HEIC; both pair with a same-named video.
var livePhotoImageExts = []string{".heic", ".heif", ".jpg", ".jpeg"}

// livePhotoVideoExts are the motion halves of a Live Photo. iPhones use .mov;
// some Takeout exports rename the QuickTime container to .mp4 (see issue #2).
var livePhotoVideoExts = []string{".mov", ".mp4"}

// DetectLivePhotoPairs scans one directory's entries and returns a map from an
// image file path to its Live Photo video partner path. A pair is two files in
// the same directory sharing a base name where one is a still image and the
// other is a motion video (e.g. IMG_1234.HEIC + IMG_1234.MOV).
//
// Pairing is by base name only — that is the sole link Google Takeout leaves
// intact after stripping Apple's ContentIdentifier. To avoid spurious matches
// the image extension must be a Live Photo still type, not just any image.
func DetectLivePhotoPairs(dirPath string, entries []os.DirEntry) map[string]string {
	// Index files by lowercased base name, separating image and video halves.
	type halves struct{ image, video string }
	byBase := make(map[string]*halves)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		full := filepath.Join(dirPath, name)

		h := byBase[base]
		if h == nil {
			h = &halves{}
			byBase[base] = h
		}
		if slices.Contains(livePhotoImageExts, ext) && h.image == "" {
			h.image = full
		} else if slices.Contains(livePhotoVideoExts, ext) && h.video == "" {
			h.video = full
		}
	}

	pairs := make(map[string]string)
	for _, h := range byBase {
		if h.image != "" && h.video != "" {
			pairs[h.image] = h.video
		}
	}
	return pairs
}

// Counts all processable files in the source path
func CountProcessableFiles(sourcePath string) (int, error) {
	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		return 0, err
	}

	if !fileInfo.IsDir() {
		return 0, fmt.Errorf("source path is not a directory")
	}

	count := 0
	subdirs, err := DiscoverDirs(sourcePath)
	if err != nil {
		return 0, err
	}

	for _, dir := range subdirs {
		files, _ := os.ReadDir(filepath.Join(sourcePath, dir.Name()))
		for _, file := range files {
			if !file.IsDir() && IsMediaFile(file.Name()) {
				count++
			}
		}
	}

	if count == 0 {
		return 0, fmt.Errorf("no media files found in folder structure")
	}
	return count, nil
}

// Detect the month of a file based on its sidecar metadata
// Returns the month as an integer between 1 and 12
func DetectFileMonth(sourcePath string, sidecarPath string) (int, error) {
	if sidecarPath != "" {
		metadata, err := ReadJsonMetadata(sidecarPath)
		if err != nil {
			return 0, err
		}

		timestamp, err := strconv.ParseInt(metadata.PhotoTakenTime.Timestamp, 10, 64)
		if err != nil {
			return 0, err
		}

		return int(time.Unix(timestamp, 0).Month()), nil
	}

	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		return 0, err
	}

	return int(fileInfo.ModTime().Month()), nil
}

func ResolveOutputDir(
	fixerCtx *FixerContext,
	sourcePath string,
	sidecarPath string,
	sourceDirName string,
	isYearFolder bool,
) (string, error) {
	if fixerCtx.Options.Flatten {
		return fixerCtx.OutputRoot, nil
	}

	targetDir := fixerCtx.OutputRoot
	if sourceDirName != "" /*&& !fixerCtx.Options.IgnoreAlbums && !isYearFolder*/ {
		targetDir = filepath.Join(targetDir, sourceDirName)
	}

	if !fixerCtx.Options.MonthSubfolders {
		return targetDir, nil
	}

	month, err := DetectFileMonth(sourcePath, sidecarPath)
	if err != nil {
		return "", err
	}

	return filepath.Join(targetDir, strconv.Itoa(month)), nil
}

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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Progress struct {
	Total     int
	Processed int
	Current   string
}

// TODO: Add more options
// TODO: Disable checkboxes when processing
type ProcessOptions struct {
	UseSymlinks         bool
	WriteMetadata       bool
	MonthSubfolders     bool
	IgnoreAlbums        bool
	Flatten             bool
	RestoreMOVExtension bool // See issue #2
	// MergeLivePhotos muxes each iPhone Live Photo pair (same-named still +
	// video) into a single Google Motion Photo file instead of writing the two
	// halves side by side. The video is embedded as a trailer in the still.
	MergeLivePhotos bool
}

type FixerContext struct {
	Ctx        context.Context
	SourceRoot string
	OutputRoot string
	Options    ProcessOptions
	ProgressCh chan<- Progress
}

// Process is the main fixer entry point.
// Process
// -> DiscoverDirs
// --> ProcessDirectory
// ---> ProcessFile
// TODO: Do something in case files already exists instead of overwriting them
func Process(
	ctx context.Context,
	sourcePath string,
	outputPath string,
	progressCh chan<- Progress,
	options ProcessOptions,
) error {
	err := InitializeFileLogger()
	if err != nil {
		if LogHandler != nil {
			LogHandler(LoggerWarn, fmt.Sprintf("Failed to initialize file logger: %v", err))
		}
	} else {
		defer func() {
			err := CloseFileLogger()
			if err != nil && LogHandler != nil {
				LogHandler(LoggerWarn, fmt.Sprintf("Failed to close file logger: %v", err))
			}
		}()
	}

	Log(LoggerInfo, "Starting processing with source: %s and output: %s", sourcePath, outputPath)

	// Log total processing time when processing is done
	startTime := time.Now()
	defer func() {
		Log(LoggerInfo, "Total processing time: %s", time.Since(startTime).Round(time.Second))
		ClearCache()
	}()

	defer close(progressCh)
	p := Progress{}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if options.WriteMetadata || options.RestoreMOVExtension {
		if err := InitializeExifTool(); err != nil {
			Log(LoggerError, "Failed to initialize exiftool: %v", err)
			return err
		}
		defer CloseExifTool()
	}

	amountImages, err := CountProcessableFiles(sourcePath)
	if err != nil {
		Log(LoggerError, "Error counting images: %v", err)
		return err
	}
	p.Total = amountImages
	progressCh <- p

	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		Log(LoggerError, "Error getting file info: %v", err)
		return err
	}

	fixerCtx := &FixerContext{
		Ctx:        ctx,
		SourceRoot: sourcePath,
		OutputRoot: outputPath,
		Options:    options,
		ProgressCh: progressCh,
	}

	// process all directories in the source directory, ignore files in the source directory itself
	// because all media files should be inside of sub-folders
	if fileInfo.IsDir() {
		dirs, err := DiscoverDirs(sourcePath)
		if err != nil {
			Log(LoggerError, "Error discovering directories: %v", err)
		}

		for _, dir := range dirs {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			dirPath := filepath.Join(sourcePath, dir.Name())
			var targetPath string = filepath.Join(outputPath, dir.Name())

			isYearFolder, err := IsYearFolder(dir.Name())
			if err != nil {
				Log(LoggerWarn, "Failed to determine if folder is a year folder for %s: %v", dir.Name(), err)
			}
			if (options.IgnoreAlbums || options.Flatten) && !isYearFolder {
				Log(LoggerInfo, "Skipping album folder: %s", dir.Name())
				continue
			}

			p = ProcessDirectory(fixerCtx, dirPath, targetPath, isYearFolder, p)
		}
	} else {
		err = ProcessFile(fixerCtx, sourcePath, "", false)
		if err != nil {
			Log(LoggerError, "Error processing file: %v", err)
		} else {
			p.Processed++
			p.Current = sourcePath
			progressCh <- p
		}
	}

	return nil
}

// mediaJob is one unit of work dispatched to a worker. videoPartner is set only
// for Live Photo pairs, in which case imagePath is the still half.
type mediaJob struct {
	imagePath    string
	videoPartner string
}

// Process a directory and fix all files within the directory. Ignores sub-directories.
func ProcessDirectory(
	fixerCtx *FixerContext,
	dirPath string,
	outputPath string,
	isYearFolder bool,
	p Progress,
) Progress {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		Log(LoggerError, "Error reading directory: %v", err)
		return p
	}

	// TODO: Fix potential race conditions
	// Job pools
	// Buffered channel to avoid blocking
	jobs := make(chan mediaJob, len(files))
	completed := make(chan string)
	// Channel to capture errors
	errors := make(chan error)

	sourceDirName := filepath.Base(dirPath)

	// Detect iPhone Live Photo pairs (same-named still + video) so each pair is
	// processed by a single worker and kept name-aligned. Symlinked album
	// entries take a different output path and are not paired here.
	var pairs map[string]string
	var pairedVideos map[string]bool
	if !fixerCtx.Options.UseSymlinks {
		pairs = DetectLivePhotoPairs(dirPath, files)
		pairedVideos = make(map[string]bool, len(pairs))
		for _, v := range pairs {
			pairedVideos[v] = true
		}
	}

	var wg sync.WaitGroup
	workerCount := runtime.NumCPU() * 2 // x2 is faster for IO tasks, x more than that has no effect based on testing

	// Start worker goroutines
	for i := 0; i < workerCount; i++ {
		go func() {
			for job := range jobs {
				if fixerCtx.Ctx.Err() != nil {
					wg.Done()
					continue
				}
				var err error
				if job.videoPartner != "" {
					err = ProcessLivePhotoPair(fixerCtx, job.imagePath, job.videoPartner, sourceDirName, isYearFolder)
				} else {
					err = ProcessFile(fixerCtx, job.imagePath, sourceDirName, isYearFolder)
				}
				if err != nil {
					errors <- fmt.Errorf("error processing file %s: %w", job.imagePath, err)
				} else {
					completed <- job.imagePath
					// A pair processes two source files in one job; report the
					// video half too so progress totals match the file count.
					if job.videoPartner != "" {
						completed <- job.videoPartner
					}
				}
				wg.Done()
			}
		}()
	}

	// Send jobs directly, add work group before transmitting job
	for _, file := range files {
		if fixerCtx.Ctx.Err() != nil {
			break
		}
		if file.IsDir() {
			continue
		}

		imagePath := filepath.Join(dirPath, file.Name())

		// Check whether a file is a media file. Sidecar JSON and known Google
		// Takeout extras are skipped silently; anything else is logged so
		// unsupported media is not dropped without a trace (issue #26).
		if !IsMediaFile(imagePath) {
			if !IsNameExtension(".json", imagePath) {
				Log(LoggerWarn, "Skipping unsupported file (not a recognized media type): %s", imagePath)
			}
			continue
		}

		// The video half of a Live Photo pair is handled alongside its image,
		// so do not dispatch it as a standalone job.
		if pairedVideos[imagePath] {
			continue
		}

		wg.Add(1)
		jobs <- mediaJob{imagePath: imagePath, videoPartner: pairs[imagePath]}
	}

	// All jobs have been sent
	close(jobs)

	// Close completed and errors channels when all jobs are finished
	go func() {
		wg.Wait()
		close(completed)
		close(errors)
	}()

	// Update progress and handle errors
	for {
		select {
		case ev, ok := <-completed:
			if !ok {
				completed = nil
			} else {
				p.Processed++
				p.Current = ev
				fixerCtx.ProgressCh <- p
			}
		case err, ok := <-errors:
			if !ok {
				errors = nil
			} else {
				Log(LoggerError, "%v", err)
			}
		case <-fixerCtx.Ctx.Done():
			// Let workers finish their current job but dont add new jobs
		}

		if completed == nil && errors == nil {
			break
		}
	}

	return p
}

// ProcessFile processes a single file by finding its sidecar file and then fixing it using the sidecar's metadata
// TODO: This function is written unorganized and should be refactored
func ProcessFile(
	fixerCtx *FixerContext,
	sourcePath string,
	sourceDirName string,
	isYearFolder bool,
) error {
	fileName := filepath.Base(sourcePath)

	// See issue #2
	if fixerCtx.Options.RestoreMOVExtension && strings.EqualFold(filepath.Ext(fileName), ".mp4") {
		majorBrand, err := GetMajorBrand(sourcePath)
		if err == nil && strings.HasPrefix(majorBrand, "Apple QuickTime") {
			ext := filepath.Ext(fileName)
			newName := fileName[:len(fileName)-len(ext)] + ".mov"
			if ext == ".MP4" {
				newName = fileName[:len(fileName)-len(ext)] + ".MOV"
			}
			fileName = newName
		}
	}

	//destPath := filepath.Join(outputPath, fileName)

	sidecarPath, err := FindSidecar(sourcePath)

	if err != nil {
		Log(LoggerError, "Error finding sidecar for file %s: %v", sourcePath, err)
		return err
	}

	// If no sidecar is found and its a video file, try to find a partner image and use it's sidecar
	if sidecarPath == "" && IsVideoFile(sourcePath) {
		partnerImage, err := FindImagePartner(sourcePath)
		if err == nil && partnerImage != "" {
			partnerSidecar, err := FindSidecar(partnerImage)
			if err == nil && partnerSidecar != "" {
				sidecarPath = partnerSidecar
			}
		}
	}

	outputDir, err := ResolveOutputDir(fixerCtx, sourcePath, sidecarPath, sourceDirName, isYearFolder)
	if err != nil {
		return err
	}

	wantedPath := filepath.Join(outputDir, fileName)

	// Resolve collisions: an identical file is skipped, a different file with
	// the same name is written alongside under a " (n)" suffix so nothing is
	// silently dropped (issue #6). Resolution and reservation are serialized
	// so concurrent workers cannot claim the same path.
	destPath, skip, reserved, err := reserveDestPath(fixerCtx, sourcePath, wantedPath)
	if err != nil {
		Log(LoggerError, "Error resolving destination for %s: %v", sourcePath, err)
		return err
	}
	if skip {
		Log(LoggerInfo, "Identical file already exists at %s, skipping %s", destPath, sourcePath)
		return nil
	}
	if destPath != wantedPath {
		Log(LoggerInfo, "Name collision for %s, writing to %s", fileName, destPath)
	}

	// Metadata sidecar file not found, copy the file without metadata
	metadataPath := sidecarPath
	if sidecarPath == "" {
		Log(LoggerWarn, "No sidecar file found for %s — copying without metadata", sourcePath)
	}

	if err := CreateFixedFile(fixerCtx, sourcePath, metadataPath, destPath, isYearFolder); err != nil {
		// A reserved placeholder must not be left behind on failure, or a
		// re-run would treat the empty file as a real collision.
		if reserved {
			os.Remove(destPath)
		}
		Log(LoggerError, "Error creating fixed file for %s: %v", sourcePath, err)
		return err
	}

	return nil
}

// ProcessLivePhotoPair fixes an iPhone Live Photo's still + motion halves
// together. Both are written into the same output directory under a single
// shared " (n)" collision suffix so their base names stay matched (which is how
// Google Photos re-pairs them on upload). When MergeLivePhotos is set, the two
// halves are then muxed into a single Google Motion Photo.
func ProcessLivePhotoPair(
	fixerCtx *FixerContext,
	imagePath string,
	videoPath string,
	sourceDirName string,
	isYearFolder bool,
) error {
	imageName := filepath.Base(imagePath)
	videoName := filepath.Base(videoPath)

	// See issue #2 — restore the QuickTime extension on the video half.
	if fixerCtx.Options.RestoreMOVExtension && strings.EqualFold(filepath.Ext(videoName), ".mp4") {
		if majorBrand, err := GetMajorBrand(videoPath); err == nil && strings.HasPrefix(majorBrand, "Apple QuickTime") {
			ext := filepath.Ext(videoName)
			newName := videoName[:len(videoName)-len(ext)] + ".mov"
			if ext == ".MP4" {
				newName = videoName[:len(videoName)-len(ext)] + ".MOV"
			}
			videoName = newName
		}
	}

	// The still half owns the sidecar; the video borrows it when it lacks one.
	imageSidecar, err := FindSidecar(imagePath)
	if err != nil {
		Log(LoggerError, "Error finding sidecar for %s: %v", imagePath, err)
		return err
	}
	videoSidecar, err := FindSidecar(videoPath)
	if err != nil {
		Log(LoggerError, "Error finding sidecar for %s: %v", videoPath, err)
		return err
	}
	if videoSidecar == "" {
		videoSidecar = imageSidecar
	}

	// Both halves share an output directory; resolve it from the still so album
	// vs. year placement is consistent across the pair.
	outputDir, err := ResolveOutputDir(fixerCtx, imagePath, imageSidecar, sourceDirName, isYearFolder)
	if err != nil {
		return err
	}
	imageWanted := filepath.Join(outputDir, imageName)
	videoWanted := filepath.Join(outputDir, videoName)

	imageDest, videoDest, skipImage, skipVideo, reservedImage, reservedVideo, err :=
		reservePairDestPaths(fixerCtx, imagePath, videoPath, imageWanted, videoWanted)
	if err != nil {
		Log(LoggerError, "Error resolving destinations for Live Photo pair %s: %v", imageName, err)
		return err
	}
	if imageDest != imageWanted {
		Log(LoggerInfo, "Name collision for Live Photo %s, writing pair to %s + %s",
			imageName, filepath.Base(imageDest), filepath.Base(videoDest))
	}

	// Write each half unless an identical copy already exists.
	if !skipImage {
		if err := CreateFixedFile(fixerCtx, imagePath, imageSidecar, imageDest, isYearFolder); err != nil {
			if reservedImage {
				os.Remove(imageDest)
			}
			if reservedVideo {
				os.Remove(videoDest)
			}
			Log(LoggerError, "Error creating fixed file for %s: %v", imagePath, err)
			return err
		}
	} else {
		Log(LoggerInfo, "Identical file already exists at %s, skipping %s", imageDest, imagePath)
	}
	if !skipVideo {
		if err := CreateFixedFile(fixerCtx, videoPath, videoSidecar, videoDest, isYearFolder); err != nil {
			if reservedVideo {
				os.Remove(videoDest)
			}
			Log(LoggerError, "Error creating fixed file for %s: %v", videoPath, err)
			return err
		}
	} else {
		Log(LoggerInfo, "Identical file already exists at %s, skipping %s", videoDest, videoPath)
	}

	// Optionally merge the pair into a single Google Motion Photo: embed the
	// video as a trailer in the still and remove the standalone video copy.
	// Requires both halves to have just been written and a muxable still.
	if fixerCtx.Options.MergeLivePhotos && !skipImage && !skipVideo {
		if !IsMotionPhotoStill(imageDest) {
			Log(LoggerInfo, "Live Photo still %s is not a muxable format; leaving pair as separate files", imageName)
		} else if err := MuxMotionPhoto(imageDest, videoDest); err != nil {
			// Leave both files in place on failure — they are still correct,
			// just not merged.
			Log(LoggerWarn, "Could not merge Live Photo %s into a Motion Photo: %v", imageName, err)
		} else if err := os.Remove(videoDest); err != nil {
			Log(LoggerWarn, "Merged Motion Photo %s but could not remove standalone video %s: %v",
				filepath.Base(imageDest), filepath.Base(videoDest), err)
		} else {
			Log(LoggerInfo, "Merged Live Photo %s into a Motion Photo", filepath.Base(imageDest))
		}
	}

	return nil
}

// reservePairDestPaths resolves and reserves output paths for a Live Photo pair
// under one shared suffix, serialized so concurrent workers cannot claim the
// same names. It mirrors reserveDestPath but reserves both halves together.
func reservePairDestPaths(fixerCtx *FixerContext, imageSrc, videoSrc, imageWanted, videoWanted string) (
	imageDest, videoDest string, skipImage, skipVideo, reservedImage, reservedVideo bool, err error,
) {
	destReserveMutex.Lock()
	defer destReserveMutex.Unlock()

	for {
		imageDest, videoDest, skipImage, skipVideo, err = ResolvePairDestPaths(imageSrc, videoSrc, imageWanted, videoWanted)
		if err != nil {
			return "", "", false, false, false, false, err
		}

		if err := os.MkdirAll(filepath.Dir(imageDest), 0755); err != nil {
			return "", "", false, false, false, false, err
		}

		// Reserve each non-skipped half with an O_EXCL placeholder. If either
		// loses a race, release any placeholder taken and re-resolve.
		reservedImage, reservedVideo = false, false
		if !skipImage {
			f, oerr := os.OpenFile(imageDest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
			if oerr != nil {
				if os.IsExist(oerr) {
					continue
				}
				return "", "", false, false, false, false, oerr
			}
			f.Close()
			reservedImage = true
		}
		if !skipVideo {
			f, oerr := os.OpenFile(videoDest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
			if oerr != nil {
				if reservedImage {
					os.Remove(imageDest)
				}
				if os.IsExist(oerr) {
					continue
				}
				return "", "", false, false, false, false, oerr
			}
			f.Close()
			reservedVideo = true
		}
		return imageDest, videoDest, skipImage, skipVideo, reservedImage, reservedVideo, nil
	}
}

// destReserveMutex serializes destination-path resolution so that two workers
// processing same-named source files cannot both claim the same output path.
var destReserveMutex sync.Mutex

// reserveDestPath resolves where sourcePath should be written and, when a new
// path is needed, reserves it by creating an empty placeholder file under lock.
// The subsequent copy overwrites the placeholder. reserved reports whether a
// placeholder file was created (so the caller can clean it up on failure).
// skip=true means an identical file already exists and nothing should be written.
func reserveDestPath(fixerCtx *FixerContext, sourcePath, wantedPath string) (destPath string, skip bool, reserved bool, err error) {
	destReserveMutex.Lock()
	defer destReserveMutex.Unlock()

	for {
		destPath, skip, err = ResolveDestPath(sourcePath, wantedPath)
		if err != nil || skip {
			return destPath, skip, false, err
		}

		// Symlinked album entries are reserved by the symlink itself, so only
		// placeholder-reserve when a real copy will happen.
		if fixerCtx.Options.UseSymlinks {
			return destPath, false, false, nil
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return "", false, false, err
		}

		f, oerr := os.OpenFile(destPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if oerr != nil {
			// Another file claimed this exact name since we resolved it; resolve
			// again now that it is taken and try to reserve the next slot.
			if os.IsExist(oerr) {
				continue
			}
			return "", false, false, oerr
		}
		f.Close()

		return destPath, false, true, nil
	}
}

func CreateFixedFile(
	fixerCtx *FixerContext,
	filePath string,
	fileMetadataPath string,
	destPath string,
	isYearFolder bool,
) error {
	// Ensure output directory exists (create if not)
	destDir := filepath.Dir(destPath)
	if _, err := os.Stat(destDir); os.IsNotExist(err) {
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return err
		}

		// Invalidate OutputRoot cache so newly created year folders are visible for symlinks
		ClearCacheDir(fixerCtx.OutputRoot)
	}

	fileName := filepath.Base(destPath)

	if fixerCtx.Options.UseSymlinks && !isYearFolder {
		monthFolder := ""
		if fixerCtx.Options.MonthSubfolders {
			month, err := DetectFileMonth(filePath, fileMetadataPath)
			if err == nil {
				monthFolder = strconv.Itoa(month)
			}
		}

		// Attempt to find the file inside of any year folder in the output
		entries, _ := ReadDirCached(fixerCtx.OutputRoot)
		for _, curEntry := range entries {
			if !curEntry.IsDir() {
				continue
			}

			isYear, _ := IsYearFolder(curEntry.Name())
			if !isYear {
				continue
			}

			targetPaths := []string{}
			if monthFolder != "" {
				targetPaths = append(targetPaths, filepath.Join(fixerCtx.OutputRoot, curEntry.Name(), monthFolder, fileName))
			}
			targetPaths = append(targetPaths, filepath.Join(fixerCtx.OutputRoot, curEntry.Name(), fileName))

			for _, target := range targetPaths {
				if _, err := os.Stat(target); err == nil {
					if err := os.Symlink(target, destPath); err != nil {
						// Symlink failed, continue with normal copy
						if !os.IsExist(err) {
							return fmt.Errorf("Failed to create symlink: %w", err)
						}
					} else {
						// Symlink successful
						return nil
					}
				}
			}
		}
	}

	if err := DuplicateFile(filePath, destPath); err != nil {
		return err
	}

	if fixerCtx.Options.WriteMetadata && fileMetadataPath != "" {
		metadata, err := ReadJsonMetadata(fileMetadataPath)
		if err != nil {
			Log(LoggerError, "Failed to read metadata from %s: %v", fileMetadataPath, err)
		} else {
			// Only apply metadata if reading was successful
			err = ApplyMetadata(destPath, metadata)
			if err != nil {
				Log(LoggerError, "Failed to apply metadata to %s: %v", destPath, err)
			}
		}
	} else if fixerCtx.Options.WriteMetadata && fileMetadataPath == "" {
		Log(LoggerInfo, "WriteMetadata enabled but no sidecar for %s — skipping metadata write", fileName)
	}

	return nil
}

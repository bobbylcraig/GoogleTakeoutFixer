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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Metadata is the human-facing information shown beneath each photo in the
// reviewer: when and where it was taken, who is in it, and basic file facts.
type Metadata struct {
	DateTaken time.Time
	HasDate   bool

	Latitude  float64
	Longitude float64
	HasGeo    bool

	People []string

	CameraMake  string
	CameraModel string
}

// exifToolPath locates a bundled exiftool next to the executable, falling back
// to one on PATH. Mirrors the fixer's lookup so a shared install is reused.
func exifToolPath() string {
	if exe, err := os.Executable(); err == nil {
		name := "exiftool"
		if runtime.GOOS == "windows" {
			name = "exiftool.exe"
		}
		bundled := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(bundled); err == nil {
			return bundled
		}
	}
	return "exiftool"
}

// ExifToolAvailable reports whether an exiftool binary can be found. The
// reviewer still works without it (showing only size/dimensions), so callers
// use this to decide whether to attempt richer metadata.
func ExifToolAvailable() bool {
	p := exifToolPath()
	if p == "exiftool" {
		_, err := exec.LookPath("exiftool")
		return err == nil
	}
	return true
}

// exifRaw mirrors the subset of exiftool's -json output we care about.
// Numeric/date fields are decoded leniently because exiftool's types vary by
// source format.
type exifRaw struct {
	DateTimeOriginal string  `json:"DateTimeOriginal"`
	CreateDate       string  `json:"CreateDate"`
	GPSLatitude      float64 `json:"GPSLatitude"`
	GPSLongitude     float64 `json:"GPSLongitude"`
	Make             string  `json:"Make"`
	Model            string  `json:"Model"`
	// People may arrive as a single string or a list depending on the tag.
	PersonInImage json.RawMessage `json:"PersonInImage"`
	Subject       json.RawMessage `json:"Subject"`
	Keywords      json.RawMessage `json:"Keywords"`
}

// ReadMetadata extracts display metadata from a single image using exiftool.
// It uses -n so GPS comes back as signed decimals and -c for stable parsing.
// On any failure it returns a zero Metadata and the error; callers treat that
// as "no rich metadata" rather than fatal.
func ReadMetadata(ctx context.Context, path string) (Metadata, error) {
	var md Metadata

	cmd := exec.CommandContext(ctx, exifToolPath(),
		"-json", "-n",
		"-DateTimeOriginal", "-CreateDate",
		"-GPSLatitude", "-GPSLongitude",
		"-Make", "-Model",
		"-PersonInImage", "-Subject", "-Keywords",
		"-charset", "filename=utf8",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return md, err
	}

	var raws []exifRaw
	if err := json.Unmarshal(out, &raws); err != nil || len(raws) == 0 {
		return md, err
	}
	r := raws[0]

	if t, ok := parseExifTime(r.DateTimeOriginal); ok {
		md.DateTaken, md.HasDate = t, true
	} else if t, ok := parseExifTime(r.CreateDate); ok {
		md.DateTaken, md.HasDate = t, true
	}

	if r.GPSLatitude != 0 || r.GPSLongitude != 0 {
		md.Latitude, md.Longitude, md.HasGeo = r.GPSLatitude, r.GPSLongitude, true
	}

	md.CameraMake = strings.TrimSpace(r.Make)
	md.CameraModel = strings.TrimSpace(r.Model)

	seen := map[string]bool{}
	for _, field := range []json.RawMessage{r.PersonInImage, r.Subject, r.Keywords} {
		for _, name := range decodeStringOrList(field) {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			md.People = append(md.People, name)
		}
	}

	return md, nil
}

// parseExifTime parses exiftool's common datetime formats. EXIF uses colons in
// the date portion ("2021:07:04 13:22:01").
func parseExifTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"2006:01:02 15:04:05",
		"2006:01:02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// decodeStringOrList handles exiftool fields that may be a JSON string or an
// array of strings (it collapses single-element lists to a bare string).
func decodeStringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	return nil
}

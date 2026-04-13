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

package version

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type VersionInfo struct {
	Version     string `json:"tag_name"`
	DownloadURL string `json:"html_url"`
}

const apiUrl = "https://api.github.com/repos/feloex/GoogleTakeoutFixer/releases/latest"

func CheckForUpdates() (VersionInfo, error) {

	currentVersion := Tag

	if currentVersion == "dev" {
		return VersionInfo{}, fmt.Errorf("Running development version, skipping")
	}

	latest, err := http.Get(apiUrl)

	if err != nil {
		return VersionInfo{}, err
	}

	var latestVersion VersionInfo

	body, err := io.ReadAll(latest.Body)
	if err != nil {
		return VersionInfo{}, err
	}

	err = json.Unmarshal(body, &latestVersion)
	if err != nil {
		return VersionInfo{}, err
	}

	isNewer := isNewerVersion(currentVersion, latestVersion.Version)

	if !isNewer {
		return VersionInfo{}, fmt.Errorf("Already using latest version (%s)", Tag)
	}

	return latestVersion, nil
}

func isNewerVersion(current, latest string) bool {

	// Remove 'v' prefix if present
	strings.TrimPrefix(current, "v")
	strings.TrimPrefix(latest, "v")

	currentParts := strings.Split(current, ".")
	latestParts := strings.Split(latest, ".")

	// versions *should* have 3 parts

	for i := 0; i < len(currentParts) && i < len(latestParts); i++ {
		if currentParts[i] < latestParts[i] {
			return true
		} else if currentParts[i] > latestParts[i] {
			return false
		}
	}

	return len(latestParts) > len(currentParts)

}

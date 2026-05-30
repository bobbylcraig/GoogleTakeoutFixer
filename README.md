# GoogleTakeoutFixer 

[![GitHub Repo stars](https://img.shields.io/github/stars/feloex/GoogleTakeoutFixer?style=flat&color=yellow&link=https%3A%2F%2Fgithub.com%2Ffeloex%2FGoogleTakeoutFixer)](https://github.com/feloex/GoogleTakeoutFixer)
[![GitHub Downloads (all assets, all releases)](https://img.shields.io/github/downloads/feloex/GoogleTakeoutFixer/total?style=flat&color=dark-green)](https://github.com/feloex/GoogleTakeoutFixer/releases)
[![GitHub Downloads (all assets, latest release)](https://img.shields.io/github/downloads/feloex/GoogleTakeoutFixer/latest/total?style=flat&color=dark-green)](https://github.com/feloex/GoogleTakeoutFixer/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/feloex/GoogleTakeoutFixer)](https://goreportcard.com/report/github.com/feloex/GoogleTakeoutFixer)

<p align="center">
    <img src="images/GoogleTakeoutFixer.png" alt="drawing" width="200"/>
</p>

A tool to easily clean and organize Google Photos Takeout exports.

## The Issue
When you download your files from Google's "Google Photos" service through "Google Takeout", the exported data is **inconsistently organized and often fragmented/broken.**
This can lead to problems:
- Files cannot be reliably sorted or grouped by date or location
- The export contains unnecessary files and a cluttered folder structure
- Your takeout having a big file size due to duplicated media and unnecessary JSON files

## Solution
GoogleTakeoutFixer solves these issues by:
- **Writing metadata** directly into your media. This includes the date taken (with the correct timezone for the photo's location), GPS location, title, description, **people/face tags**, and **favorite status** (written as a 5-star rating).
- **Organizing your files** into a clear and structured folder structure for easier navigation.
- **Automatically removing unnecessary JSON files**.

Supported media types: `.jpg`, `.jpeg`, `.png`, `.heic`, `.gif`, `.webp`, `.tiff`, `.bmp`, `.mp4`, `.mov`, `.avi`, `.mkv`, `.m4v`, `.3gp`. Files in other formats are skipped, but the skip is now logged (instead of happening silently) so you can see what was left behind.

## Preview
<p align="center">
    <img src="images/GTFWindow-v1.3.0.png" alt="GoogleTakeoutFixer Window" width="460"/>
</p>

## Tutorial
### 1. Preparation
To use GoogleTakeoutFixer, you must have downloaded your photos from Google Takeout and extracted them. Follow these steps:

1. Go to [takeout.google.com](https://takeout.google.com/) and click "Deselect all".

    <img src="images/DeselectAllTakeout.png" alt="Google Takeout deselect button" width="400"/>
2. Scroll down and select "Google Photos".

    <img src="images/TakeoutPhotosSelect.png" alt="Google Takeout Selected" width="400"/>
3. Scroll down to the bottom and click "Next Step".

4. In the "Transfer to" section, choose how you'd like to receive your download link. I recommend choosing email. For "File size", select 50 GB for easier handling.

    <img src="images/CreateExportTakeout.png" alt="Create Export options" width=300>
5. Click "Create export" and follow the instructions.

> [!NOTE]
> - If your Google Takeout exceeds the 50 GB limit and is split into multiple archives, extract all the archives and move the extracted files into a single folder. This ensures that GoogleTakeoutFixer can process all your files correctly.
> - Select the folder named "Google Photos" as your input folder. This folder should contain subfolders like "Photos from (year)" and folders with the names of your albums. Do not select a parent folder of "Google Photos".

### 2. Installation
1. Download the latest release of GoogleTakeoutFixer from the [release page](https://github.com/feloex/GoogleTakeoutFixer/releases). Choose the version that matches your operating system.
2. Extract the downloaded archive.
3. Run the executable file.

> [!IMPORTANT]
> When running the executable, a window about security can pop-up if you are using Windows. **Click "more info" and "run anyway"**.

### 3. Using GoogleTakeoutFixer
1. Click **"Select Google Takeout folder"** and choose the folder where you extracted your Google Takeout photos. This folder is named something like "Google Photos".
2. Click **"Select output folder"** and choose the folder where you want the fixed photos to be saved.
3. Choose the options that you want to apply:
    - **"Write metadata"**: Writes metadata from JSON files into the media files. May not be necessary.
    - **"Use symlinks for albums"**: Creates file links instead of duplicating files for albums.
    - **"Ignore album folders"**: Ignores album folders and only processes year folders.
    - **"Create month subfolders"**: Creates month subfolders (labeled 1-12) inside of the output folders.
    - **"Flatten output structure"**: Puts all files directly in the output folder.
    - **"Restore .MOV file extension"**: Restores .MOV file extension in case the Major Brand EXIF field says "Apple QuickTime (.MOV/QT)" (See #2).
    - **"Merge Live Photos"**: Rejoins iPhone Live Photos that Takeout split into a separate still (HEIC/JPEG) and video (MOV/MP4) sharing the same name. Each pair is merged back into a single [Google Motion Photo](https://developer.android.com/media/platform/motion-photo-format) so Google Photos plays it as a Live Photo on re-upload. Cannot be combined with symlinked albums (merging rewrites the still in place).
5. Click **"Start processing"** and wait for the process to finish. The time it takes depends on the number of photos and videos you have.

Once the process is complete, you can find your fixed files in the output folder you selected.

You will be able to find a full log file inside of the GoogleTakeoutFixer folder inside of the `logs` folder.

---

### CLI usage
You can also use GoogleTakeoutFixer through the CLI. Use the following flags:
- `--input "PATH"`: Path to Google takeout directory
- `--output "PATH"`: Path to output directory
- `--symlink`: Use symlinks inside of albums instead of duplicating images
- `--skip-metadata`: Skip writing metadata to files
- `--ignore-albums`: Ignore album folders and only process year folders
- `--month-subfolders`: Create month subfolders (labeled 1-12) inside of folders
- `--flatten`: Flatten the folder structure and put all files directly in the output folder
- `--restore-mov`: Restore .MOV file extension in case the Major Brand EXIF field says \"Apple QuickTime (.MOV/QT)\" (See #2)
- `--merge-live-photos`: Merge split iPhone Live Photo pairs (HEIC/JPEG + MOV/MP4) back into a single Google Motion Photo so Google Photos plays them as Live Photos. Cannot be combined with `--symlink`.
- `--version`: Show version
- `--help`: Show help message

Example usage:
```sh
./GoogleTakeoutFixer --input "/path/to/takeout/Google Photos/" --output "/path/to/output/folder/" --symlink
``` 

You might have to give the executable permissions to run on Linux and macOS using `chmod +x GoogleTakeoutFixer` before you can run it through the terminal.

## Duplicate Finder
GoogleTakeoutFixer ships with a separate **Duplicate Finder** tool that scans a folder for duplicate and near-duplicate photos. It catches more than byte-identical copies:
- The **same photo in different formats** (e.g. a JPG and a PNG of the same image)
- **Recompressed** copies (a smaller, lower-quality version of the same photo)
- The **same photo at different resolutions**
- **Mild edits** such as brightness/colour adjustments (at a higher sensitivity)

How it works:
1. Choose a folder to scan and a **sensitivity** preset (Identical, Near-duplicate, or Similar / possible edits).
2. The tool presents each match as a side-by-side pair, with each photo's file size, dimensions, date taken, GPS location, and people tags shown beneath it.
3. For each pair you can **Delete left**, **Delete right**, **Delete both**, or **Keep both**.

Deletions are never permanent: matched files are **moved to a `GTF-duplicates-trash` folder** inside the scanned directory, and a `restore-manifest.json` records where each file came from so you can put it back. Because perceptual matching can occasionally over-group low-detail images (skies, blank walls), review each pair before deleting.

> [!NOTE]
> People and location metadata are read using ExifTool. If ExifTool is not installed/bundled, the finder still works but shows only file size and dimensions.

## Development
### Setup
This project uses [Go](https://go.dev/) as the programming language and [Fyne](https://fyne.io/) as the GUI framework. To run this programm in a developement enviroment, `cd` into the `cmd` directory and run `go run .` to start the program. 
To run the GUI, make sure you have the necessary dependencies for Fyne installed. See the [Fyne Prerequisites](https://docs.fyne.io/started/quick/#prerequisites).

```
# Clone this repo
git clone https://github.com/feloex/GoogleTakeoutFixer.git
cd GoogleTakeoutFixer

# cd into the entrypoint directory and run the main.go file
cd cmd
go run .
```

The **Duplicate Finder** is a separate binary with its own entry point. Run it from a development environment with:

```
# from the repo root
cd cmd/dedupe
go run .
```

## License
GoogleTakeoutFixer itself is licensed under GNU General Public License v3. See [LICENSE](LICENSE) for more details.

Used libaries may use different licenses.

This project modifies metadata using the [ExifTool](https://exiftool.org/) library by **Phil Harvey**. ExifTool is licensed under the Perl Artistic license, or the GNU General Public License (see [here](https://exiftool.org/#license) for more details).

AMD64 Windows users are provided with a copy of the library "Mesa3D" (`opengl32.dll`). This is mostly licensed under the MIT License, but some files may be licensed under different licenses. See [here](https://docs.mesa3d.org/license.html) for more details. A copy of the MIT license may be obtained [here](https://opensource.org/license/mit/). It is also provided as `MESA-LICENSE.txt` in the corresponding release assets.

## Donate
This software is completely free. You are free to use, modify, and distribute it. If you'd like to support my work, you can donate via my monero adress. Remember that donating is completely optional.
Please consider supporting other open-source projects.

XMR: ``86ApiK5RFKeVsaEDreQvvkE5Mdo6p3xwtGAZTcbf7JKFDnJ4bG52zqsZjAzgFW6prWhfarinBLrCpW8faxKotG26RcRD4fQ``

## Disclaimer
Not affiliated with Google LLC.

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

// Package dedupegui is the two-pane duplicate reviewer. It scans a folder for
// duplicate and near-duplicate photos, shows each pair side by side with their
// metadata, and lets the user trash the unwanted copies (recoverably).
package dedupegui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/feloex/GoogleTakeoutFixer/internal/dedupe"
	"github.com/ncruces/zenity"
)

// pair is one left/right comparison drawn from a duplicate group. Within a
// group of N files we compare the first (the reference we lean toward keeping)
// against each of the others, so the user reviews N-1 pairs per group.
type pair struct {
	group dedupe.Group
	left  dedupe.PhotoInfo
	right dedupe.PhotoInfo
}

// reviewer holds the mutable UI state for one scan session.
type reviewer struct {
	win   fyne.Window
	trash *dedupe.Trash

	pairs   []pair
	current int
	// trashed tracks paths already moved to the trash. A file can appear in
	// more than one pair (groups of 3+ compare every other file against one
	// reference), so without this a second pair would try to trash a file that
	// is already gone and stall the review.
	trashed map[string]bool

	leftImg, rightImg   *canvas.Image
	leftMeta, rightMeta *widget.Label
	headerLabel         *widget.Label
	matchLabel          *widget.Label

	actions fyne.CanvasObject // delete/keep button row, hidden when no pairs
}

func Main() {
	a := app.New()
	w := a.NewWindow("GoogleTakeoutFixer — Duplicate Finder")
	w.Resize(fyne.NewSize(900, 640))

	r := &reviewer{win: w}
	w.SetContent(r.buildStartView())
	w.ShowAndRun()
}

// buildStartView is the initial screen: pick a folder, pick a sensitivity, scan.
func (r *reviewer) buildStartView() fyne.CanvasObject {
	var folder string

	presets := dedupe.Presets()
	names := make([]string, len(presets))
	for i, p := range presets {
		names[i] = fmt.Sprintf("%s — %s", p.Name, p.Description)
	}
	sensSelect := widget.NewSelect(names, nil)
	sensSelect.SetSelectedIndex(1) // Near-duplicate by default

	autoExactCheck := widget.NewCheck(
		"Auto-delete byte-identical duplicates without asking", nil)

	folderLabel := widget.NewLabel("No folder selected")
	folderLabel.Truncation = fyne.TextTruncateEllipsis

	var folderBtn *widget.Button
	folderBtn = widget.NewButtonWithIcon("Select folder to scan", theme.FolderOpenIcon(), func() {
		dir, err := zenity.SelectFile(zenity.Title("Select folder to scan for duplicates"), zenity.Directory())
		if err == nil {
			folder = dir
			folderLabel.SetText(folder)
		}
	})

	scanBtn := widget.NewButtonWithIcon("Scan", theme.SearchIcon(), func() {
		if folder == "" {
			dialog.ShowInformation("Select a folder", "Please choose a folder to scan first.", r.win)
			return
		}
		r.runScan(folder, presets[sensSelect.SelectedIndex()], autoExactCheck.Checked)
	})
	scanBtn.Importance = widget.HighImportance

	note := widget.NewLabel(
		"Duplicates are moved to a trash folder, never permanently deleted.\n" +
			"A restore manifest is written so you can undo any removal.")
	note.Wrapping = fyne.TextWrapWord

	form := container.NewVBox(
		widget.NewLabelWithStyle("Find duplicate & similar photos", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		folderBtn,
		folderLabel,
		widget.NewLabel("Sensitivity:"),
		sensSelect,
		autoExactCheck,
		scanBtn,
		widget.NewSeparator(),
		note,
	)
	return container.NewPadded(form)
}

// runScan scans in the background and shows progress, then switches to the
// reviewer (or an "all clear" message).
func (r *reviewer) runScan(folder string, sens dedupe.Sensitivity, autoExact bool) {
	progress := widget.NewProgressBar()
	status := widget.NewLabel("Scanning…")
	status.Truncation = fyne.TextTruncateEllipsis
	r.win.SetContent(container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("Scanning "+filepath.Base(folder), fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		progress, status,
	)))

	progressCh := make(chan dedupe.ScanProgress, 64)
	go func() {
		for p := range progressCh {
			p := p
			fyne.Do(func() {
				if p.Total > 0 {
					progress.Max = float64(p.Total)
					progress.SetValue(float64(p.Processed))
				}
				status.SetText(fmt.Sprintf("%d/%d  %s", p.Processed, p.Total, filepath.Base(p.Current)))
			})
		}
	}()

	go func() {
		photos, err := dedupe.ScanDirectory(context.Background(), folder, progressCh)
		if err != nil {
			fyne.Do(func() { dialog.ShowError(err, r.win) })
			return
		}
		groups := dedupe.FindGroups(photos, sens)

		trash, err := dedupe.NewTrash(filepath.Join(folder, "GTF-duplicates-trash"))
		if err != nil {
			fyne.Do(func() { dialog.ShowError(err, r.win) })
			return
		}

		// Optionally collapse byte-identical groups without prompting. This
		// runs here, off the UI thread, because each Move touches the disk.
		// Exact groups that are fully handled are dropped from review.
		autoDeleted := 0
		if autoExact {
			groups, autoDeleted, err = autoCollapseExact(groups, trash)
			if err != nil {
				fyne.Do(func() { dialog.ShowError(err, r.win) })
				return
			}
		}

		fyne.Do(func() {
			r.trash = trash
			r.pairs = pairsFromGroups(groups)
			r.trashed = make(map[string]bool)
			r.current = 0
			if len(r.pairs) == 0 {
				msg := "No duplicate or similar photos matched the chosen sensitivity."
				if autoDeleted > 0 {
					msg = fmt.Sprintf(
						"%d byte-identical duplicate(s) were moved to:\n%s\n\nNo further matches need review.",
						autoDeleted, trash.Dir())
				}
				dialog.ShowInformation("Nothing left to review", msg, r.win)
				r.win.SetContent(r.buildStartView())
				return
			}
			if autoDeleted > 0 {
				dialog.ShowInformation("Exact duplicates removed",
					fmt.Sprintf("%d byte-identical duplicate(s) were moved to the trash folder. Review the remaining matches below.",
						autoDeleted), r.win)
			}
			r.win.SetContent(r.buildReviewView())
			r.showPair()
		})
	}()
}

// autoCollapseExact trashes all-but-one of every byte-identical (MatchExact)
// group, keeping the shortest-path copy, and returns the groups that still need
// human review (perceptual matches, plus any exact group a trash error left
// only partially handled) along with the number of files moved.
func autoCollapseExact(groups []dedupe.Group, trash *dedupe.Trash) ([]dedupe.Group, int, error) {
	var remaining []dedupe.Group
	deleted := 0
	for _, g := range groups {
		if g.Kind != dedupe.MatchExact || len(g.Files) < 2 {
			remaining = append(remaining, g)
			continue
		}
		_, toTrash := dedupe.KeepCandidate(g)
		for _, f := range toTrash {
			if _, err := trash.Move(f.Path); err != nil {
				return nil, deleted, fmt.Errorf("auto-deleting %s: %w", filepath.Base(f.Path), err)
			}
			deleted++
		}
	}
	return remaining, deleted, nil
}

// pairsFromGroups flattens groups into reviewable left/right pairs.
func pairsFromGroups(groups []dedupe.Group) []pair {
	var out []pair
	for _, g := range groups {
		if len(g.Files) < 2 {
			continue
		}
		ref := g.Files[0]
		for _, other := range g.Files[1:] {
			out = append(out, pair{group: g, left: ref, right: other})
		}
	}
	return out
}

// buildReviewView lays out the two-pane comparison with action buttons.
func (r *reviewer) buildReviewView() fyne.CanvasObject {
	r.headerLabel = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	r.matchLabel = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{})

	r.leftImg = canvas.NewImageFromResource(nil)
	r.leftImg.FillMode = canvas.ImageFillContain
	r.leftImg.SetMinSize(fyne.NewSize(360, 360))
	r.rightImg = canvas.NewImageFromResource(nil)
	r.rightImg.FillMode = canvas.ImageFillContain
	r.rightImg.SetMinSize(fyne.NewSize(360, 360))

	r.leftMeta = widget.NewLabel("")
	r.leftMeta.Wrapping = fyne.TextWrapWord
	r.rightMeta = widget.NewLabel("")
	r.rightMeta.Wrapping = fyne.TextWrapWord

	leftPane := container.NewBorder(nil, r.leftMeta, nil, nil, r.leftImg)
	rightPane := container.NewBorder(nil, r.rightMeta, nil, nil, r.rightImg)
	panes := container.NewGridWithColumns(2, leftPane, rightPane)

	delLeft := widget.NewButton("Delete left", func() { r.act(true, false) })
	delRight := widget.NewButton("Delete right", func() { r.act(false, true) })
	delBoth := widget.NewButton("Delete both", func() { r.act(true, true) })
	keepBoth := widget.NewButtonWithIcon("Keep both", theme.ConfirmIcon(), func() { r.act(false, false) })
	delLeft.Importance = widget.DangerImportance
	delRight.Importance = widget.DangerImportance
	delBoth.Importance = widget.DangerImportance
	keepBoth.Importance = widget.HighImportance
	r.actions = container.NewGridWithColumns(4, delLeft, delRight, delBoth, keepBoth)

	top := container.NewVBox(r.headerLabel, r.matchLabel)
	return container.NewPadded(container.NewBorder(top, r.actions, nil, nil, panes))
}

// showPair renders the current pair's images and metadata.
func (r *reviewer) showPair() {
	p := r.pairs[r.current]

	r.headerLabel.SetText(fmt.Sprintf("Pair %d of %d", r.current+1, len(r.pairs)))
	matchDesc := p.group.Kind.String()
	if p.group.Kind == dedupe.MatchPerceptual {
		matchDesc = fmt.Sprintf("%s (distance %d)", matchDesc,
			p.left.DHash.HammingDistance(p.right.DHash))
	}
	r.matchLabel.SetText(matchDesc)

	r.setPane(r.leftImg, r.leftMeta, p.left)
	r.setPane(r.rightImg, r.rightMeta, p.right)
}

// setPane loads an image file into a canvas and fills its metadata label.
// Metadata is read off the UI thread because exiftool spawns a process.
func (r *reviewer) setPane(img *canvas.Image, label *widget.Label, info dedupe.PhotoInfo) {
	loaded := canvas.NewImageFromFile(info.Path)
	img.Resource = loaded.Resource
	img.File = info.Path
	img.Refresh()

	label.SetText(basicInfo(info) + "\nReading metadata…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		md, err := dedupe.ReadMetadata(ctx, info.Path)
		text := basicInfo(info)
		if err == nil {
			text += "\n" + richInfo(md)
		}
		fyne.Do(func() { label.SetText(text) })
	}()
}

// act applies the user's decision, trashing the chosen sides, then advances.
func (r *reviewer) act(deleteLeft, deleteRight bool) {
	p := r.pairs[r.current]
	var toTrash []string
	if deleteLeft {
		toTrash = append(toTrash, p.left.Path)
	}
	if deleteRight {
		toTrash = append(toTrash, p.right.Path)
	}

	for _, path := range toTrash {
		// Already moved in an earlier pair (groups of 3+ reuse files); nothing
		// to do, and re-trashing would fail on the missing source.
		if r.trashed[path] {
			continue
		}
		if _, err := r.trash.Move(path); err != nil {
			dialog.ShowError(fmt.Errorf("could not trash %s: %w", filepath.Base(path), err), r.win)
			return
		}
		r.trashed[path] = true
	}
	r.advance()
}

// advance moves to the next reviewable pair, or shows a completion summary at
// the end. Pairs whose files were already trashed in an earlier decision are
// skipped so the user never sees a comparison against a missing file.
func (r *reviewer) advance() {
	for {
		r.current++
		if r.current >= len(r.pairs) {
			n := len(r.trash.Entries())
			dialog.ShowInformation("Review complete",
				fmt.Sprintf("Reviewed all pairs. %d file(s) moved to:\n%s", n, r.trash.Dir()),
				r.win)
			r.win.SetContent(r.buildStartView())
			return
		}
		p := r.pairs[r.current]
		if r.trashed[p.left.Path] || r.trashed[p.right.Path] {
			continue
		}
		r.showPair()
		return
	}
}

// basicInfo is the always-available facts (no exiftool needed).
func basicInfo(info dedupe.PhotoInfo) string {
	dims := "unknown"
	if info.Decoded {
		dims = fmt.Sprintf("%d×%d", info.Width, info.Height)
	}
	return fmt.Sprintf("%s\n%s · %s",
		filepath.Base(info.Path), humanSize(info.Size), dims)
}

// richInfo formats the exiftool-derived metadata.
func richInfo(md dedupe.Metadata) string {
	out := ""
	if md.HasDate {
		out += "Date: " + md.DateTaken.Format("2006-01-02 15:04") + "\n"
	}
	if md.HasGeo {
		out += fmt.Sprintf("Location: %.5f, %.5f\n", md.Latitude, md.Longitude)
	}
	if len(md.People) > 0 {
		out += "People: "
		for i, p := range md.People {
			if i > 0 {
				out += ", "
			}
			out += p
		}
		out += "\n"
	}
	if md.CameraMake != "" || md.CameraModel != "" {
		out += "Camera: " + md.CameraMake + " " + md.CameraModel + "\n"
	}
	if out == "" {
		out = "No embedded metadata"
	}
	return out
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

package fixer

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveDestPath_NoCollision(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	writeFile(t, src, "A")

	dest := filepath.Join(dir, "out.jpg")
	got, skip, err := ResolveDestPath(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("expected skip=false for free path")
	}
	if got != dest {
		t.Fatalf("got %q, want %q", got, dest)
	}
}

func TestResolveDestPath_IdenticalSkips(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	dest := filepath.Join(dir, "out.jpg")
	writeFile(t, src, "same bytes")
	writeFile(t, dest, "same bytes")

	got, skip, err := ResolveDestPath(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if !skip {
		t.Fatal("expected skip=true for identical content")
	}
	if got != dest {
		t.Fatalf("got %q, want %q", got, dest)
	}
}

func TestResolveDestPath_DifferentGetsSuffix(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	dest := filepath.Join(dir, "out.jpg")
	writeFile(t, src, "new content")
	writeFile(t, dest, "old content")

	got, skip, err := ResolveDestPath(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("expected skip=false for differing content")
	}
	want := filepath.Join(dir, "out (1).jpg")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveDestPath_SkipsToIdenticalNumberedVariant(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	dest := filepath.Join(dir, "out.jpg")
	writeFile(t, src, "variant two")
	writeFile(t, dest, "original")
	writeFile(t, filepath.Join(dir, "out (1).jpg"), "variant two")

	got, skip, err := ResolveDestPath(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if !skip {
		t.Fatalf("expected skip=true; identical copy exists at variant, got path %q", got)
	}
}

func TestResolveDestPath_MultipleDifferentVariants(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	dest := filepath.Join(dir, "out.jpg")
	writeFile(t, src, "third distinct")
	writeFile(t, dest, "first")
	writeFile(t, filepath.Join(dir, "out (1).jpg"), "second")

	got, skip, err := ResolveDestPath(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("expected skip=false")
	}
	want := filepath.Join(dir, "out (2).jpg")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestIsMediaFile_NewExtensions(t *testing.T) {
	for _, name := range []string{"a.3gp", "b.m4v", "c.gif", "d.webp", "e.tiff", "f.GIF"} {
		if !IsMediaFile(name) {
			t.Errorf("expected %q to be recognized as media", name)
		}
	}
	if IsMediaFile("notes.txt") {
		t.Error("txt should not be media")
	}
	if IsMediaFile("photo.json") {
		t.Error("json should not be media")
	}
}

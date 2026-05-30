package dedupe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrash_MoveAndRestore(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "photos")
	trashDir := filepath.Join(root, "trash")
	os.MkdirAll(srcDir, 0755)

	src := filepath.Join(srcDir, "dup.jpg")
	if err := os.WriteFile(src, []byte("contents"), 0644); err != nil {
		t.Fatal(err)
	}

	tr, err := NewTrash(trashDir)
	if err != nil {
		t.Fatal(err)
	}

	entry, err := tr.Move(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source should be gone after trashing")
	}
	if _, err := os.Stat(entry.TrashedPath); err != nil {
		t.Fatalf("trashed file missing: %v", err)
	}

	// Manifest should persist across reload.
	tr2, err := NewTrash(trashDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr2.Entries()) != 1 {
		t.Fatalf("expected 1 manifest entry after reload, got %d", len(tr2.Entries()))
	}

	if err := tr2.Restore(entry.TrashedPath); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if string(data) != "contents" {
		t.Fatalf("restored contents wrong: %q", data)
	}
	if len(tr2.Entries()) != 0 {
		t.Fatalf("manifest should be empty after restore, got %d", len(tr2.Entries()))
	}
}

func TestTrash_NameCollision(t *testing.T) {
	root := t.TempDir()
	trashDir := filepath.Join(root, "trash")
	tr, err := NewTrash(trashDir)
	if err != nil {
		t.Fatal(err)
	}

	// Two files with the same base name from different folders.
	for _, sub := range []string{"a", "b"} {
		d := filepath.Join(root, sub)
		os.MkdirAll(d, 0755)
		p := filepath.Join(d, "IMG.jpg")
		os.WriteFile(p, []byte(sub), 0644)
		if _, err := tr.Move(p); err != nil {
			t.Fatal(err)
		}
	}

	entries := tr.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].TrashedPath == entries[1].TrashedPath {
		t.Fatal("colliding names were not disambiguated")
	}
	if _, err := os.Stat(entries[0].TrashedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entries[1].TrashedPath); err != nil {
		t.Fatal(err)
	}
}

func TestTrash_RestoreRefusesClobber(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "x.jpg")
	os.WriteFile(src, []byte("v1"), 0644)

	tr, err := NewTrash(filepath.Join(root, "trash"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := tr.Move(src)
	if err != nil {
		t.Fatal(err)
	}
	// A new file reappears at the original path.
	os.WriteFile(src, []byte("v2"), 0644)

	if err := tr.Restore(entry.TrashedPath); err == nil {
		t.Fatal("expected restore to refuse clobbering an existing file")
	}
	if data, _ := os.ReadFile(src); string(data) != "v2" {
		t.Fatal("existing file was overwritten despite refusal")
	}
}

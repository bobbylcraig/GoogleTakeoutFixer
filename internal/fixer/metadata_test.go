package fixer

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func baseMeta() imageMetadata {
	var m imageMetadata
	m.PhotoTakenTime.Timestamp = "1609459200" // 2021-01-01 00:00:00 UTC
	return m
}

func hasArg(args []string, want string) bool {
	return slices.Contains(args, want)
}

func TestBuildExifArgs_People(t *testing.T) {
	m := baseMeta()
	m.People = []struct {
		Name string `json:"name"`
	}{{Name: "Alice"}, {Name: "Bob"}, {Name: "  "}, {Name: ""}}

	args, _, err := buildExifArgs(m)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"Alice", "Bob"} {
		if !hasArg(args, "-XMP:PersonInImage+="+name) {
			t.Errorf("missing PersonInImage for %s", name)
		}
		if !hasArg(args, "-XMP-dc:Subject+="+name) {
			t.Errorf("missing Subject for %s", name)
		}
		if !hasArg(args, "-IPTC:Keywords+="+name) {
			t.Errorf("missing Keywords for %s", name)
		}
	}

	// Blank/whitespace names must be skipped entirely.
	for _, a := range args {
		if strings.HasSuffix(a, "+=") || strings.HasSuffix(a, "+=  ") {
			t.Errorf("blank person name produced an arg: %q", a)
		}
	}
}

func TestBuildExifArgs_Favorited(t *testing.T) {
	m := baseMeta()
	m.Favorited = true
	args, _, err := buildExifArgs(m)
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(args, "-XMP:Rating=5") {
		t.Error("favorited should set -XMP:Rating=5")
	}

	m.Favorited = false
	args, _, _ = buildExifArgs(m)
	if hasArg(args, "-XMP:Rating=5") {
		t.Error("non-favorited should not set a rating")
	}
}

func TestBuildExifArgs_InvalidTimestamp(t *testing.T) {
	var m imageMetadata
	m.PhotoTakenTime.Timestamp = "not-a-number"
	if _, _, err := buildExifArgs(m); err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
}

// TestParseSidecarFields confirms the new struct fields decode from a realistic
// Google Takeout JSON payload.
func TestParseSidecarFields(t *testing.T) {
	raw := `{
		"title": "IMG_0001.jpg",
		"description": "beach day",
		"photoTakenTime": {"timestamp": "1609459200", "formatted": "Jan 1, 2021"},
		"geoData": {"latitude": 40.5, "longitude": -3.7, "altitude": 600},
		"people": [{"name": "Alice"}, {"name": "Bob"}],
		"favorited": true
	}`

	var m imageMetadata
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.People) != 2 || m.People[0].Name != "Alice" || m.People[1].Name != "Bob" {
		t.Errorf("people not parsed: %+v", m.People)
	}
	if !m.Favorited {
		t.Error("favorited not parsed")
	}
	if m.Description != "beach day" {
		t.Errorf("description not parsed: %q", m.Description)
	}
}

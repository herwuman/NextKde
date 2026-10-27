package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// newTestFileIndex returns a Service whose index is already populated, so the
// search paths answer from the snapshot instead of walking the real home
// directory.
func newTestFileIndex(entries ...fileEntry) *Service {
	s := &Service{files: newFileIndex()}
	s.files.entries = entries
	s.files.ready = true
	return s
}

func TestFuzzyScoreRequiresOrderedSubsequence(t *testing.T) {
	if _, ok := fuzzyScore("notes", "notes.md"); !ok {
		t.Fatalf("a prefix match must score")
	}
	if _, ok := fuzzyScore("notes", "seton.md"); ok {
		t.Fatalf("characters out of order must not match")
	}
	if _, ok := fuzzyScore("notes", "note"); ok {
		t.Fatalf("a missing character must not match")
	}
	if score, ok := fuzzyScore("", "anything"); !ok || score != 1 {
		t.Fatalf("empty query = %d, %v; want 1, true", score, ok)
	}
}

func TestFuzzyScorePrefersConsecutiveAndWordBoundaryHits(t *testing.T) {
	direct, ok := fuzzyScore("note", "notes.md")
	if !ok {
		t.Fatalf("expected a match for notes.md")
	}
	// The letters of "note" appear in "annotatex.md" only out of order and
	// never at a word boundary, so it must score below the prefix match.
	scattered, ok := fuzzyScore("note", "annotatex.md")
	if !ok {
		t.Fatalf("expected a match for annotatex.md")
	}
	if direct <= scattered {
		t.Fatalf("notes.md scored %d, annotated scored %d; want the prefix match higher",
			direct, scattered)
	}

	boundary, _ := fuzzyScore("note", "my-notes.md")
	inside, _ := fuzzyScore("note", "mynotesx.md")
	if boundary <= inside {
		t.Fatalf("word-boundary match scored %d, mid-word scored %d; want the boundary match higher",
			boundary, inside)
	}
}

func TestBuildFileIndexSkipsHiddenAndNoiseDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile := func(relative string) {
		t.Helper()
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFile("Documents/note.md")
	writeFile(".hidden.txt")
	writeFile(".git/config")
	writeFile("node_modules/pkg/index.js")
	writeFile("project/vendor/lib.go")
	writeFile("project/__pycache__/mod.pyc")

	entries := buildFileIndex(root)
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	if !seen["note.md"] {
		t.Fatalf("a visible document is missing from %#v", entries)
	}
	for _, skipped := range []string{"hidden.txt", "config", "index.js", "lib.go", "mod.pyc"} {
		if seen[skipped] {
			t.Fatalf("%q must not be indexed: %#v", skipped, entries)
		}
	}
}

func TestBuildFileIndexStopsBeforeDeepTrees(t *testing.T) {
	root := t.TempDir()
	// A directory at exactly the depth limit is walked; one level past it is
	// not. fileIndexMaxDepth counts the components below the root.
	atLimit := root
	for i := 0; i < fileIndexMaxDepth; i++ {
		atLimit = filepath.Join(atLimit, "a")
	}
	if err := os.MkdirAll(atLimit, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(atLimit, "at-limit.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	pastLimit := filepath.Join(atLimit, "a")
	if err := os.MkdirAll(pastLimit, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pastLimit, "too-deep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	seen := map[string]bool{}
	for _, entry := range buildFileIndex(root) {
		seen[entry.Name] = true
	}
	if !seen["at-limit.txt"] {
		t.Fatalf("a file at the depth limit must be indexed")
	}
	if seen["too-deep.txt"] {
		t.Fatalf("a file past the depth limit must not be indexed")
	}
}

func TestRefreshFileIndexPopulatesFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "resume.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := &Service{files: newFileIndex()}
	s.refreshFileIndex()

	s.files.mu.RLock()
	ready := s.files.ready
	entries := append([]fileEntry(nil), s.files.entries...)
	s.files.mu.RUnlock()
	if !ready {
		t.Fatalf("index must be marked ready after a build")
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "resume.pdf" {
			if entry.Dir != home || entry.Path != filepath.Join(home, "resume.pdf") {
				t.Fatalf("unexpected entry: %#v", entry)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("home file missing from %#v", entries)
	}
}

func TestSearchFilesEmptyQueryReturnsNewestFirst(t *testing.T) {
	s := newTestFileIndex(
		fileEntry{Name: "old.txt", Path: "/home/u/old.txt", Mod: 100},
		fileEntry{Name: "new.txt", Path: "/home/u/new.txt", Mod: 300},
		fileEntry{Name: "mid.txt", Path: "/home/u/mid.txt", Mod: 200},
	)
	results, indexed, ready := s.searchFiles("   ", 20)
	if !ready || indexed != 3 {
		t.Fatalf("ready = %v, indexed = %d; want true, 3", ready, indexed)
	}
	if len(results) != 3 || results[0].Name != "new.txt" || results[2].Name != "old.txt" {
		t.Fatalf("unexpected order: %#v", results)
	}
}

func TestSearchFilesRanksMatchesAndClampsLimit(t *testing.T) {
	entries := make([]fileEntry, 0, 120)
	for i := 0; i < 120; i++ {
		name := "file-" + string(rune('a'+i%26)) + "-" + strconv.Itoa(i) + ".txt"
		entries = append(entries, fileEntry{Name: name, Path: "/home/u/" + name, Mod: int64(i)})
	}
	entries = append(entries, fileEntry{Name: "notes.md", Path: "/home/u/notes.md", Mod: 1})
	s := newTestFileIndex(entries...)

	results, indexed, ready := s.searchFiles("notes", 1000)
	if !ready || indexed != len(entries) {
		t.Fatalf("ready = %v, indexed = %d; want true, %d", ready, indexed, len(entries))
	}
	if len(results) == 0 || results[0].Name != "notes.md" {
		t.Fatalf("notes.md must rank first: %#v", results)
	}
	if len(results) > 100 {
		t.Fatalf("limit must be clamped to 100, got %d", len(results))
	}

	byDefault, _, _ := s.searchFiles("file", 0)
	if len(byDefault) > 20 {
		t.Fatalf("a non-positive limit must fall back to 20, got %d", len(byDefault))
	}
}

func TestResolveFileKeepsAnExistingPath(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "shelf.txt")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := newTestFileIndex()

	resolved, found := s.resolveFile(existing, "shelf.txt")
	if !found || resolved != existing {
		t.Fatalf("resolved = %q, %v; want %q, true", resolved, found, existing)
	}
}

func TestResolveFileFindsAMovedFileByName(t *testing.T) {
	dir := t.TempDir()
	moved := filepath.Join(dir, "moved.txt")
	s := newTestFileIndex(
		fileEntry{Name: "moved.txt", Path: filepath.Join(dir, "older.txt"), Mod: 100},
	)
	s.files.entries[0].Path = moved
	// The index has to answer for the current location so the comparison the
	// protocol reports ("moved") is exercised against a real file.
	if err := os.WriteFile(moved, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	resolved, found := s.resolveFile(filepath.Join(dir, "gone.txt"), "moved.txt")
	if !found || resolved != moved {
		t.Fatalf("resolved = %q, %v; want %q, true", resolved, found, moved)
	}
}

func TestResolveFileReportsAMissingFile(t *testing.T) {
	dir := t.TempDir()
	s := newTestFileIndex(fileEntry{Name: "other.txt", Path: filepath.Join(dir, "other.txt"), Mod: 10})

	resolved, found := s.resolveFile(filepath.Join(dir, "gone.txt"), "gone.txt")
	if found {
		t.Fatalf("a name that is not indexed must not resolve, got %q", resolved)
	}
	if resolved != filepath.Join(dir, "gone.txt") {
		t.Fatalf("resolved = %q; want the requested path echoed back", resolved)
	}
}

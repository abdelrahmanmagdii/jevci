package benchmark

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/abdelrahmanmagdii/jevci/internal/gitdiff"
)

func TestRevertSet(t *testing.T) {
	changes := []gitdiff.FileChange{
		{Path: "pkg/a.go", Status: gitdiff.Modified},
		{Path: "pkg/new.go", Status: gitdiff.Added},
		{Path: "pkg/a_test.go", Status: gitdiff.Modified},     // test: excluded
		{Path: "vendor/x/y.go", Status: gitdiff.Modified},     // vendor: excluded
		{Path: "pkg/testdata/z.go", Status: gitdiff.Modified}, // testdata: excluded
		{Path: "docs/readme.txt", Status: gitdiff.Modified},   // not .go: excluded
		{Path: "pkg/gone.go", Status: gitdiff.Deleted},
		{Path: "pkg/new_name.go", OldPath: "pkg/old_name.go", Status: gitdiff.Renamed},
	}
	restore, remove := revertSet(changes)
	wantRestore := []string{"pkg/a.go", "pkg/gone.go", "pkg/old_name.go"}
	wantRemove := []string{"pkg/new.go", "pkg/new_name.go"}
	if !reflect.DeepEqual(restore, wantRestore) {
		t.Fatalf("restore %v want %v", restore, wantRestore)
	}
	if !reflect.DeepEqual(remove, wantRemove) {
		t.Fatalf("remove %v want %v", remove, wantRemove)
	}
}

func TestLoadSuiteDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(p, []byte("repo: github.com/x/y\nworkdir: /tmp/w\nprs:\n  - number: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSuite(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Oracle != "head" || len(s.Packages) != 1 || s.Packages[0] != "./..." || s.TestTimeout == 0 {
		t.Fatalf("%+v", s)
	}
	if err := os.WriteFile(p, []byte("repo: x\nworkdir: w\noracle: bogus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(p); err == nil {
		t.Fatal("want oracle validation error")
	}
}

func TestParseStrategies(t *testing.T) {
	specs, err := parseStrategies([]string{"full", "jevci:no-direct"})
	if err != nil {
		t.Fatal(err)
	}
	if specs[1].label != "jevci:no-direct" || !specs[1].noDirect {
		t.Fatalf("%+v", specs[1])
	}
	if _, err := parseStrategies([]string{"nope"}); err == nil {
		t.Fatal("want error")
	}
}

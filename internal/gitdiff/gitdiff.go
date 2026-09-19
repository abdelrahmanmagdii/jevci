// Package gitdiff provides git plumbing and diff parsing for jevci.
package gitdiff

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ChangeStatus describes how a file changed between two revisions.
type ChangeStatus int

const (
	Added ChangeStatus = iota
	Modified
	Deleted
	Renamed
	Copied
	TypeChanged
)

func (s ChangeStatus) String() string {
	switch s {
	case Added:
		return "A"
	case Modified:
		return "M"
	case Deleted:
		return "D"
	case Renamed:
		return "R"
	case Copied:
		return "C"
	case TypeChanged:
		return "T"
	}
	return "?"
}

// FileChange is one path in a diff. For renames Path is the new path and
// OldPath the old path.
type FileChange struct {
	Path    string
	OldPath string
	Status  ChangeStatus
}

// Repo is a git working tree.
type Repo struct {
	Dir string
}

func (r *Repo) git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func revHint(rev string) string {
	return fmt.Sprintf("rev %q not found; in CI run `git fetch origin <branch>` (shallow clones need --depth or fetch-depth: 0)", rev)
}

// Open resolves the repository root containing dir.
func Open(dir string) (*Repo, error) {
	r := &Repo{Dir: dir}
	out, err := r.git("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("open repo at %s: %w", dir, err)
	}
	root := strings.TrimSpace(string(out))
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Repo{Dir: root}, nil
}

// ResolveRev resolves a revision to a commit SHA.
func (r *Repo) ResolveRev(rev string) (string, error) {
	out, err := r.git("rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s: %w", revHint(rev), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the merge base of base and head.
func (r *Repo) MergeBase(base, head string) (string, error) {
	out, err := r.git("merge-base", base, head)
	if err != nil {
		return "", fmt.Errorf("merge-base %s %s: %w; %s", base, head, err, revHint(base))
	}
	return strings.TrimSpace(string(out)), nil
}

// Changes lists file changes between from and to using NUL-separated
// name-status output with rename detection.
func (r *Repo) Changes(from, to string) ([]FileChange, error) {
	out, err := r.git("diff", "--name-status", "-M", "-z", from, to)
	if err != nil {
		return nil, fmt.Errorf("diff %s..%s: %w; %s", from, to, err, revHint(from))
	}
	fields := strings.Split(string(out), "\x00")
	var changes []FileChange
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "" {
			continue
		}
		code := f[0]
		switch code {
		case 'R', 'C':
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("malformed name-status output near %q", f)
			}
			st := Renamed
			if code == 'C' {
				st = Copied
			}
			changes = append(changes, FileChange{OldPath: fields[i+1], Path: fields[i+2], Status: st})
			i += 2
		default:
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("malformed name-status output near %q", f)
			}
			var st ChangeStatus
			switch code {
			case 'A':
				st = Added
			case 'M':
				st = Modified
			case 'D':
				st = Deleted
			case 'T':
				st = TypeChanged
			default:
				return nil, fmt.Errorf("unknown change status %q", f)
			}
			changes = append(changes, FileChange{Path: fields[i+1], Status: st})
			i++
		}
	}
	return changes, nil
}

func OnlyModifiedFiles(changes []FileChange, paths []string) ([]FileChange, error) {
	if len(paths) == 0 {
		return changes, nil
	}
	want := map[string]bool{}
	for _, path := range paths {
		if want[path] {
			return nil, fmt.Errorf("duplicate source replay path %q", path)
		}
		want[path] = true
	}
	var selected []FileChange
	for _, c := range changes {
		if !want[c.Path] {
			continue
		}
		if c.Status != Modified {
			return nil, fmt.Errorf("source replay path %q must be a modified file", c.Path)
		}
		selected = append(selected, c)
	}
	if len(selected) != len(paths) {
		return nil, fmt.Errorf("source replay paths must all appear in the diff")
	}
	return selected, nil
}

// Diff returns the unified diff between from and to limited to paths with
// contextLines of context.
func (r *Repo) Diff(from, to string, paths []string, contextLines int) (string, error) {
	args := []string{"diff", fmt.Sprintf("-U%d", contextLines), from, to}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := r.git(args...)
	if err != nil {
		return "", fmt.Errorf("diff %s..%s: %w", from, to, err)
	}
	return string(out), nil
}

// ShowFile returns the contents of path at rev.
func (r *Repo) ShowFile(rev, path string) ([]byte, error) {
	out, err := r.git("show", rev+":"+path)
	if err != nil {
		return nil, fmt.Errorf("show %s:%s: %w", rev, path, err)
	}
	return out, nil
}

// Hunk describes one @@ hunk of a unified diff.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
}

// ParseHunks parses unified diff text into per-file hunk lists keyed by
// the post-image path (old path for deleted files).
func ParseHunks(unifiedDiff string) map[string][]Hunk {
	out := map[string][]Hunk{}
	var cur, oldPath string
	for _, line := range strings.Split(unifiedDiff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			p := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			p = strings.TrimPrefix(p, "a/")
			oldPath = ""
			if p != "/dev/null" {
				oldPath = p
			}
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			p = strings.TrimPrefix(p, "b/")
			if p == "/dev/null" {
				cur = oldPath // deleted file: key by old path
			} else {
				cur = p
			}
			if cur != "" {
				if _, ok := out[cur]; !ok {
					out[cur] = nil
				}
			}
		case strings.HasPrefix(line, "@@"):
			if cur == "" {
				continue
			}
			h, ok := parseHunkHeader(line)
			if ok {
				out[cur] = append(out[cur], h)
			}
		}
	}
	return out
}

func parseHunkHeader(line string) (Hunk, bool) {
	var h Hunk
	body := strings.TrimPrefix(line, "@@")
	end := strings.Index(body, "@@")
	if end < 0 {
		return h, false
	}
	parts := strings.Fields(body[:end])
	if len(parts) < 2 {
		return h, false
	}
	var ok bool
	h.OldStart, h.OldLines, ok = parseRange(parts[0], '-')
	if !ok {
		return h, false
	}
	h.NewStart, h.NewLines, ok = parseRange(parts[1], '+')
	return h, ok
}

func parseRange(s string, prefix byte) (start, lines int, ok bool) {
	if s == "" || s[0] != prefix {
		return 0, 0, false
	}
	s = s[1:]
	if i := strings.IndexByte(s, ','); i >= 0 {
		if _, err := fmt.Sscanf(s[:i], "%d", &start); err != nil {
			return 0, 0, false
		}
		if _, err := fmt.Sscanf(s[i+1:], "%d", &lines); err != nil {
			return 0, 0, false
		}
		return start, lines, true
	}
	if _, err := fmt.Sscanf(s, "%d", &start); err != nil {
		return 0, 0, false
	}
	return start, 1, true
}

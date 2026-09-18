package benchmark

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// EnsureClone clones url into dir when absent, else fetches all remotes.
func EnsureClone(ctx context.Context, url, dir string) error {
	if _, err := os.Stat(dir + "/.git"); err == nil {
		_, err := git(ctx, dir, "fetch", "--all", "--tags", "--prune")
		return err
	}
	if !strings.Contains(url, "://") && !strings.HasPrefix(url, "git@") {
		url = "https://" + url
	}
	_, err := git(ctx, "", "clone", "-q", url, dir)
	return err
}

// Checkout detaches the working tree at sha.
func Checkout(ctx context.Context, dir, sha string) error {
	_, err := git(ctx, dir, "checkout", "-q", "--detach", sha)
	return err
}

// FetchPRHead fetches pull/<pr>/head and returns its SHA.
func FetchPRHead(ctx context.Context, dir string, pr int) (string, error) {
	ref := fmt.Sprintf("refs/jevci/pr/%d", pr)
	if _, err := git(ctx, dir, "fetch", "-q", "origin", fmt.Sprintf("pull/%d/head:%s", pr, ref)); err != nil {
		return "", err
	}
	out, err := git(ctx, dir, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the merge base of a and b in dir.
func MergeBase(ctx context.Context, dir, a, b string) (string, error) {
	out, err := git(ctx, dir, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

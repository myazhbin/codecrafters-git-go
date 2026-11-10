package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles creates the given files (path -> content) below the current
// directory, creating parent directories as needed.
func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// realGit runs the official git in dir ignoring the user's git configuration, and returns its
// trimmed output. The test is skipped when git is not installed.
func realGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runWriteTree(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	if err := run("write-tree", nil, &out); err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	hash := strings.TrimSuffix(out.String(), "\n")
	if len(hash) != 40 || out.String() != hash+"\n" {
		t.Fatalf("output = %q, want a 40-character hash and a newline", out.String())
	}
	return hash
}

func TestWriteTreeEmpty(t *testing.T) {
	inTempRepo(t)
	// The well-known hash of the empty tree.
	if got := runWriteTree(t); got != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Errorf("hash = %s", got)
	}
}

func TestWriteTreeContents(t *testing.T) {
	inTempRepo(t)
	writeFiles(t, map[string]string{
		"test_file_1.txt":            "hello world\n",
		"test_dir_1/test_file_2.txt": "hello world\n",
		"test_dir_2/test_file_3.txt": "something else\n",
	})

	hash := runWriteTree(t)

	var out bytes.Buffer
	if err := run("ls-tree", []string{"--name-only", hash}, &out); err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	if want := "test_dir_1\ntest_dir_2\ntest_file_1.txt\n"; out.String() != want {
		t.Errorf("ls-tree = %q, want %q", out.String(), want)
	}

	_, content, err := readObject(".git", hash)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseTree(content)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Mode != modeDir || entries[2].Mode != modeFile {
		t.Errorf("unexpected modes: %+v", entries)
	}
	// The blob for the file must be readable and intact.
	out.Reset()
	if err := run("cat-file", []string{"-p", entries[2].Hash}, &out); err != nil {
		t.Fatalf("cat-file: %v", err)
	}
	if out.String() != "hello world\n" {
		t.Errorf("blob content = %q", out.String())
	}
}

// TestWriteTreeMatchesGit compares our hash against the official git
// implementation for a tree exercising sort order, modes and nesting.
func TestWriteTreeMatchesGit(t *testing.T) {
	dir := inTempRepo(t)
	writeFiles(t, map[string]string{
		"file.txt":        "hello world\n",
		"foo/inner.txt":   "inner\n",
		"foo.txt":         "sorts before the foo directory\n",
		"foo-bar":         "also sorts before foo/\n",
		"a/b/c/deep.txt":  "deep\n",
		"Upper.txt":       "uppercase sorts first\n",
		"dir/one.txt":     "1\n",
		"dir/two.txt":     "2\n",
		"empty-file":      "",
		"bin/script.sh":   "#!/bin/sh\necho hi\n",
		"binary.dat":      "\x00\x01\x02\xff",
		"zeta/alpha/file": "z\n",
	})
	if err := os.Chmod("bin/script.sh", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", "link"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("empty-dir/nested", 0755); err != nil {
		t.Fatal(err)
	}

	got := runWriteTree(t)

	// Build the expected tree with real git in a separate object store so it
	// cannot see (or reuse) the objects we just wrote.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "real.git"))
	t.Setenv("GIT_WORK_TREE", dir)
	realGit(t, dir, "init", "-q")
	realGit(t, dir, "-c", "core.autocrlf=false", "add", "-A", ".")
	want := realGit(t, dir, "write-tree")

	if got != want {
		t.Errorf("write-tree = %s, official git = %s", got, want)
	}
}

func TestWriteTreeErrors(t *testing.T) {
	inTempRepo(t)
	if err := run("write-tree", []string{"extra"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected an error for unexpected arguments")
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// fixClock pins the commit clock and identity for deterministic output.
func fixClock(t *testing.T) {
	t.Helper()
	old := now
	now = func() time.Time { return time.Unix(1234567890, 0).UTC() }
	t.Cleanup(func() { now = old })
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "")
	}
}

func runCommitTree(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run("commit-tree", args, &out); err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	hash := strings.TrimSuffix(out.String(), "\n")
	if !isHexHash(hash) || out.String() != hash+"\n" {
		t.Fatalf("output = %q, want a 40-character hash and a newline", out.String())
	}
	return hash
}

func TestCommitTreeWithParent(t *testing.T) {
	inTempRepo(t)
	fixClock(t)
	tree := runWriteTree(t)
	parent := runCommitTree(t, tree, "-m", "Initial commit")

	hash := runCommitTree(t, tree, "-p", parent, "-m", "Second commit")

	objType, content, err := readObject(".git", hash)
	if err != nil {
		t.Fatal(err)
	}
	if objType != "commit" {
		t.Errorf("type = %q, want commit", objType)
	}
	want := "tree " + tree + "\n" +
		"parent " + parent + "\n" +
		"author John Doe <john@example.com> 1234567890 +0000\n" +
		"committer John Doe <john@example.com> 1234567890 +0000\n" +
		"\n" +
		"Second commit\n"
	if string(content) != want {
		t.Errorf("commit content =\n%s\nwant\n%s", content, want)
	}
}

func TestCommitTreeWithoutParent(t *testing.T) {
	inTempRepo(t)
	fixClock(t)
	hash := runCommitTree(t, emptyTree, "-m", "Initial commit")

	_, content, err := readObject(".git", hash)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "parent ") {
		t.Errorf("root commit must not have a parent line:\n%s", content)
	}
	if !strings.HasSuffix(string(content), "\n\nInitial commit\n") {
		t.Errorf("unexpected message in:\n%s", content)
	}
}

func TestCommitTreeArgumentOrder(t *testing.T) {
	inTempRepo(t)
	fixClock(t)
	parent := runCommitTree(t, emptyTree, "-m", "first")

	a := runCommitTree(t, emptyTree, "-p", parent, "-m", "msg")
	b := runCommitTree(t, "-m", "msg", "-p", parent, emptyTree)
	if a != b {
		t.Errorf("argument order changed the commit: %s vs %s", a, b)
	}
}

func TestCommitTreeUsesEnvIdentity(t *testing.T) {
	inTempRepo(t)
	fixClock(t)
	t.Setenv("GIT_AUTHOR_NAME", "Ada")
	t.Setenv("GIT_AUTHOR_EMAIL", "ada@example.com")

	hash := runCommitTree(t, emptyTree, "-m", "msg")
	_, content, err := readObject(".git", hash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "author Ada <ada@example.com> 1234567890 +0000\n") ||
		!strings.Contains(string(content), "committer John Doe <john@example.com> 1234567890 +0000\n") {
		t.Errorf("unexpected identity lines:\n%s", content)
	}
}

func TestSignatureTimezone(t *testing.T) {
	sig := signature{"A", "a@b.c", time.Unix(1234567890, 0).In(time.FixedZone("", -(5*3600 + 30*60)))}
	if got, want := sig.String(), "A <a@b.c> 1234567890 -0530"; got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// TestCommitTreeMatchesGit checks that the official git computes the same
// hash for the same inputs and can read the commit we wrote.
func TestCommitTreeMatchesGit(t *testing.T) {
	dir := inTempRepo(t)
	fixClock(t)
	writeFiles(t, map[string]string{"test.txt": "hello world\n"})
	tree := runWriteTree(t)
	parent := runCommitTree(t, tree, "-m", "Initial commit")
	hash := runCommitTree(t, tree, "-p", parent, "-m", "Second commit")

	t.Setenv("GIT_AUTHOR_NAME", "John Doe")
	t.Setenv("GIT_AUTHOR_EMAIL", "john@example.com")
	t.Setenv("GIT_AUTHOR_DATE", "1234567890 +0000")
	t.Setenv("GIT_COMMITTER_NAME", "John Doe")
	t.Setenv("GIT_COMMITTER_EMAIL", "john@example.com")
	t.Setenv("GIT_COMMITTER_DATE", "1234567890 +0000")
	if want := realGit(t, dir, "commit-tree", tree, "-p", parent, "-m", "Second commit"); hash != want {
		t.Errorf("commit-tree = %s, official git = %s", hash, want)
	}

	shown := realGit(t, dir, "show", "-s", "--format=%T %P %an <%ae> %s", hash)
	if want := tree + " " + parent + " John Doe <john@example.com> Second commit"; shown != want {
		t.Errorf("git show = %q, want %q", shown, want)
	}
	realGit(t, dir, "fsck", "--no-dangling")
}

func TestCommitTreeErrors(t *testing.T) {
	inTempRepo(t)
	for name, args := range map[string][]string{
		"no args":         {},
		"no message":      {emptyTree},
		"no tree":         {"-m", "msg"},
		"missing -m arg":  {emptyTree, "-m"},
		"missing -p arg":  {emptyTree, "-m", "msg", "-p"},
		"invalid tree":    {"nope", "-m", "msg"},
		"invalid parent":  {emptyTree, "-p", "nope", "-m", "msg"},
		"two trees":       {emptyTree, emptyTree, "-m", "msg"},
		"uppercase hash":  {strings.ToUpper(emptyTree), "-m", "msg"},
		"hash wrong size": {emptyTree[:39], "-m", "msg"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run("commit-tree", args, &out); err == nil {
				t.Fatal("expected an error")
			}
			if out.Len() != 0 {
				t.Errorf("expected no output, got %q", out.String())
			}
		})
	}
}

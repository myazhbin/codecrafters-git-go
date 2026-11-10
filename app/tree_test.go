package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// rawTree builds tree object content from entries, in the order given.
func rawTree(t *testing.T, entries []treeEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, e := range entries {
		sha, err := hex.DecodeString(e.Hash)
		if err != nil {
			t.Fatal(err)
		}
		buf.WriteString(e.Mode + " " + e.Name)
		buf.WriteByte(0)
		buf.Write(sha)
	}
	return buf.Bytes()
}

// sampleTree writes a tree with two directories and two files and returns
// its hash along with the entries it contains.
func sampleTree(t *testing.T) (string, []treeEntry) {
	t.Helper()
	blob := writeRawObject(t, "blob", []byte("hello world\n"))
	script := writeRawObject(t, "blob", []byte("#!/bin/sh\n"))
	sub := writeRawObject(t, "tree", rawTree(t, []treeEntry{{modeFile, "file_in_dir", blob}}))

	entries := []treeEntry{
		{modeDir, "dir1", sub},
		{modeDir, "dir2", sub},
		{modeFile, "file1", blob},
		{modeExecutable, "run.sh", script},
	}
	return writeRawObject(t, "tree", rawTree(t, entries)), entries
}

func TestLsTreeNameOnly(t *testing.T) {
	inTempRepo(t)
	hash, _ := sampleTree(t)

	var out bytes.Buffer
	if err := run("ls-tree", []string{"--name-only", hash}, &out); err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	if want := "dir1\ndir2\nfile1\nrun.sh\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestLsTreeFull(t *testing.T) {
	inTempRepo(t)
	hash, entries := sampleTree(t)

	var out bytes.Buffer
	if err := run("ls-tree", []string{hash}, &out); err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	want := "040000 tree " + entries[0].Hash + "\tdir1\n" +
		"040000 tree " + entries[1].Hash + "\tdir2\n" +
		"100644 blob " + entries[2].Hash + "\tfile1\n" +
		"100755 blob " + entries[3].Hash + "\trun.sh\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestLsTreeEmpty(t *testing.T) {
	inTempRepo(t)
	hash := writeRawObject(t, "tree", nil)

	var out bytes.Buffer
	if err := run("ls-tree", []string{"--name-only", hash}, &out); err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}
}

func TestLsTreeErrors(t *testing.T) {
	inTempRepo(t)
	blob := writeRawObject(t, "blob", []byte("x"))

	for name, args := range map[string][]string{
		"no args":        {},
		"only flag":      {"--name-only"},
		"not a tree":     {"--name-only", blob},
		"missing object": {"--name-only", "0000000000000000000000000000000000000000"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run("ls-tree", args, &bytes.Buffer{}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseTreeMalformed(t *testing.T) {
	for name, raw := range map[string]string{
		"no space":       "100644",
		"no nul":         "100644 file",
		"truncated hash": "100644 file\x00short",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTree([]byte(raw)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

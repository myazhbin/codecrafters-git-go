package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// inTempRepo switches into a fresh temporary directory, runs "init" there and
// restores the previous working directory when the test finishes.
func inTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := run("init", nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("init: %v", err)
	}
	return dir
}

// writeRawObject stores an object the way real git would, independently of
// the implementation under test, and returns its hash.
func writeRawObject(t *testing.T, objType string, content []byte) string {
	t.Helper()
	raw := append([]byte(fmt.Sprintf("%s %d\x00", objType, len(content))), content...)
	sum := sha1.Sum(raw)
	hash := hex.EncodeToString(sum[:])

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(".git", "objects", hash[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hash[2:]), buf.Bytes(), 0444); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var out bytes.Buffer
	if err := run("init", nil, &out); err != nil {
		t.Fatalf("init: %v", err)
	}

	for _, d := range []string{".git", ".git/objects", ".git/refs"} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist", d)
		}
	}
	head, err := os.ReadFile(".git/HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if string(head) != "ref: refs/heads/main\n" {
		t.Errorf("HEAD = %q", head)
	}
	if out.String() != "Initialized git directory\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run("bogus", nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}

func TestCatFileBlob(t *testing.T) {
	inTempRepo(t)

	cases := map[string][]byte{
		"simple":           []byte("hello world"),
		"trailing newline": []byte("hello world\n"),
		"empty":            {},
		"binary":           {0, 1, 2, 0, 255, '\n', 0},
		"multiline":        []byte("mango apple\nblueberry\n\norange"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			hash := writeRawObject(t, "blob", content)

			var out bytes.Buffer
			if err := run("cat-file", []string{"-p", hash}, &out); err != nil {
				t.Fatalf("cat-file: %v", err)
			}
			if !bytes.Equal(out.Bytes(), content) {
				t.Errorf("output = %q, want %q", out.Bytes(), content)
			}
		})
	}
}

func TestCatFileErrors(t *testing.T) {
	inTempRepo(t)
	hash := writeRawObject(t, "blob", []byte("x"))

	cases := map[string][]string{
		"no args":        {},
		"missing hash":   {"-p"},
		"unknown flag":   {"-x", hash},
		"short hash":     {"-p", "abc"},
		"missing object": {"-p", "0000000000000000000000000000000000000000"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run("cat-file", args, &out); err == nil {
				t.Fatal("expected an error")
			}
			if out.Len() != 0 {
				t.Errorf("expected no output, got %q", out.String())
			}
		})
	}
}

func TestParseObject(t *testing.T) {
	typ, content, err := parseObject([]byte("blob 11\x00hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if typ != "blob" || string(content) != "hello world" {
		t.Errorf("got (%q, %q)", typ, content)
	}

	for name, raw := range map[string]string{
		"no nul":        "blob 3abc",
		"no space":      "blob3\x00abc",
		"bad size":      "blob x\x00abc",
		"size mismatch": "blob 5\x00abc",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseObject([]byte(raw)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

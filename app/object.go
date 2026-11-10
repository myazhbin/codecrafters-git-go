package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// objectPath returns the location of a loose object inside gitDir.
func objectPath(gitDir, hash string) (string, error) {
	if len(hash) != 40 {
		return "", fmt.Errorf("not a valid object name %s", hash)
	}
	return filepath.Join(gitDir, "objects", hash[:2], hash[2:]), nil
}

// readObject reads and decompresses the loose object with the given hash,
// returning its type ("blob", "tree", "commit") and its content.
func readObject(gitDir, hash string) (objType string, content []byte, err error) {
	path, err := objectPath(gitDir, hash)
	if err != nil {
		return "", nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("not a valid object name %s", hash)
	}
	defer f.Close()

	zr, err := zlib.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("decompressing object %s: %w", hash, err)
	}
	defer zr.Close()

	raw, err := io.ReadAll(zr)
	if err != nil {
		return "", nil, fmt.Errorf("decompressing object %s: %w", hash, err)
	}

	return parseObject(raw)
}

// parseObject splits a decompressed object ("<type> <size>\0<content>")
// into its type and content, validating the declared size.
func parseObject(raw []byte) (objType string, content []byte, err error) {
	nul := bytes.IndexByte(raw, 0)
	if nul < 0 {
		return "", nil, fmt.Errorf("malformed object: missing header terminator")
	}

	header, content := raw[:nul], raw[nul+1:]
	typ, sizeStr, ok := bytes.Cut(header, []byte(" "))
	if !ok {
		return "", nil, fmt.Errorf("malformed object header %q", header)
	}

	size, err := strconv.Atoi(string(sizeStr))
	if err != nil {
		return "", nil, fmt.Errorf("malformed object size %q", sizeStr)
	}
	if size != len(content) {
		return "", nil, fmt.Errorf("object size mismatch: header says %d, got %d", size, len(content))
	}

	return string(typ), content, nil
}

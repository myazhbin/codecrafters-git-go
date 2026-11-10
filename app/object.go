package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
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

// hashObject returns the hex object hash and the raw (uncompressed)
// representation of an object with the given type and content.
func hashObject(objType string, content []byte) (hash string, raw []byte) {
	raw = append([]byte(fmt.Sprintf("%s %d\x00", objType, len(content))), content...)
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:]), raw
}

// writeObject stores an object as a zlib-compressed loose object inside
// gitDir and returns its hash. Existing objects are left untouched.
func writeObject(gitDir, objType string, content []byte) (string, error) {
	hash, raw := hashObject(objType, content)
	path, err := objectPath(gitDir, hash)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	}

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	// Write to a temporary file first so a crash never leaves a partial object.
	tmp, err := os.CreateTemp(filepath.Dir(path), "tmp_obj_")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0444); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return hash, nil
}

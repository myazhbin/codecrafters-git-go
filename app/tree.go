package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Tree entry modes as stored by git.
const (
	modeDir        = "40000"
	modeFile       = "100644"
	modeExecutable = "100755"
	modeSymlink    = "120000"
	modeSubmodule  = "160000"
)

// treeEntry is a single entry of a tree object.
type treeEntry struct {
	Mode string // as stored in the object, e.g. "40000" or "100644"
	Name string
	Hash string // 40-character hex hash
}

// Type returns the object type the entry points to.
func (e treeEntry) Type() string {
	switch e.Mode {
	case modeDir:
		return "tree"
	case modeSubmodule:
		return "commit"
	default:
		return "blob"
	}
}

// parseTree decodes the content of a tree object: a sequence of
// "<mode> <name>\0<20-byte hash>" entries.
func parseTree(content []byte) ([]treeEntry, error) {
	var entries []treeEntry
	for len(content) > 0 {
		space := bytes.IndexByte(content, ' ')
		if space < 0 {
			return nil, fmt.Errorf("malformed tree entry: missing mode")
		}
		mode := string(content[:space])
		content = content[space+1:]

		nul := bytes.IndexByte(content, 0)
		if nul < 0 {
			return nil, fmt.Errorf("malformed tree entry: missing name terminator")
		}
		name := string(content[:nul])
		content = content[nul+1:]

		if len(content) < 20 {
			return nil, fmt.Errorf("malformed tree entry: truncated hash")
		}
		entries = append(entries, treeEntry{
			Mode: mode,
			Name: name,
			Hash: hex.EncodeToString(content[:20]),
		})
		content = content[20:]
	}
	return entries, nil
}

// encodeTree serializes entries into tree object content, sorted the way
// git sorts them: bytewise by name, with directories compared as "name/".
func encodeTree(entries []treeEntry) ([]byte, error) {
	sorted := append([]treeEntry(nil), entries...)
	sortKey := func(e treeEntry) string {
		if e.Mode == modeDir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(sorted, func(i, j int) bool { return sortKey(sorted[i]) < sortKey(sorted[j]) })

	var buf bytes.Buffer
	for _, e := range sorted {
		sha, err := hex.DecodeString(e.Hash)
		if err != nil || len(sha) != 20 {
			return nil, fmt.Errorf("invalid hash %q for tree entry %s", e.Hash, e.Name)
		}
		buf.WriteString(e.Mode)
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
		buf.WriteByte(0)
		buf.Write(sha)
	}
	return buf.Bytes(), nil
}

// writeTree recursively stores dir as a tree object in gitDir and returns
// its hash. Every file is treated as staged; the .git directory is skipped
// and, like git, directories without any files are not recorded. The second
// return value reports whether the tree has no entries.
func writeTree(gitDir, dir string) (hash string, empty bool, err error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, err
	}

	var entries []treeEntry
	for _, de := range dirEntries {
		name := de.Name()
		if name == ".git" {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := de.Info()
		if err != nil {
			return "", false, err
		}

		switch {
		case info.IsDir():
			subHash, subEmpty, err := writeTree(gitDir, path)
			if err != nil {
				return "", false, err
			}
			if subEmpty {
				continue
			}
			entries = append(entries, treeEntry{modeDir, name, subHash})

		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return "", false, err
			}
			blobHash, err := writeObject(gitDir, "blob", []byte(target))
			if err != nil {
				return "", false, err
			}
			entries = append(entries, treeEntry{modeSymlink, name, blobHash})

		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return "", false, err
			}
			blobHash, err := writeObject(gitDir, "blob", content)
			if err != nil {
				return "", false, err
			}
			mode := modeFile
			if info.Mode()&0100 != 0 {
				mode = modeExecutable
			}
			entries = append(entries, treeEntry{mode, name, blobHash})
		}
	}

	content, err := encodeTree(entries)
	if err != nil {
		return "", false, err
	}
	hash, err = writeObject(gitDir, "tree", content)
	return hash, len(entries) == 0, err
}

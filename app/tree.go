package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
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

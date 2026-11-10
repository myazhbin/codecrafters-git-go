package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

// Object type codes used inside packfiles.
const (
	packCommit   = 1
	packTree     = 2
	packBlob     = 3
	packTag      = 4
	packOfsDelta = 6
	packRefDelta = 7
)

var packTypeNames = map[int]string{
	packCommit: "commit",
	packTree:   "tree",
	packBlob:   "blob",
	packTag:    "tag",
}

// packEntry is one object read from a packfile. For delta entries Data holds
// the delta instructions until the entry is resolved against its base.
type packEntry struct {
	Offset     int64
	Type       string // object type; empty while an unresolved delta
	Data       []byte
	BaseOffset int64  // for ofs-delta entries
	BaseHash   string // for ref-delta entries
	isDelta    bool
}

// parsePack decodes a version 2 packfile into its entries, verifying the
// trailing checksum. Delta entries are returned unresolved.
func parsePack(pack []byte) ([]*packEntry, error) {
	if len(pack) < 12+sha1.Size {
		return nil, fmt.Errorf("packfile too short")
	}
	if string(pack[:4]) != "PACK" {
		return nil, fmt.Errorf("bad packfile signature %q", pack[:4])
	}
	if version := binary.BigEndian.Uint32(pack[4:8]); version != 2 {
		return nil, fmt.Errorf("unsupported packfile version %d", version)
	}
	count := binary.BigEndian.Uint32(pack[8:12])

	body := pack[:len(pack)-sha1.Size]
	if sum := sha1.Sum(body); !bytes.Equal(sum[:], pack[len(body):]) {
		return nil, fmt.Errorf("packfile checksum mismatch")
	}

	// bytes.Reader is an io.ByteReader, so the zlib reader consumes exactly
	// the compressed bytes of each object and leaves r at the next entry.
	r := bytes.NewReader(body)
	if _, err := r.Seek(12, io.SeekStart); err != nil {
		return nil, err
	}

	entries := make([]*packEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		offset := int64(len(body)) - int64(r.Len())
		entry, err := readPackEntry(r, offset)
		if err != nil {
			return nil, fmt.Errorf("pack object %d at offset %d: %w", i, offset, err)
		}
		entries = append(entries, entry)
	}
	if r.Len() != 0 {
		return nil, fmt.Errorf("%d unexpected trailing bytes in packfile", r.Len())
	}
	return entries, nil
}

func readPackEntry(r *bytes.Reader, offset int64) (*packEntry, error) {
	// Header: 3-bit type and a variable-length size, 4 bits in the first
	// byte and 7 bits in each continuation byte (least significant first).
	b, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	typ := int(b>>4) & 7
	size := uint64(b & 0x0f)
	for shift := uint(4); b&0x80 != 0; shift += 7 {
		if b, err = r.ReadByte(); err != nil {
			return nil, err
		}
		size |= uint64(b&0x7f) << shift
	}

	entry := &packEntry{Offset: offset}
	switch typ {
	case packCommit, packTree, packBlob, packTag:
		entry.Type = packTypeNames[typ]

	case packOfsDelta:
		// Distance back to the base object, in git's offset encoding.
		if b, err = r.ReadByte(); err != nil {
			return nil, err
		}
		distance := int64(b & 0x7f)
		for b&0x80 != 0 {
			if b, err = r.ReadByte(); err != nil {
				return nil, err
			}
			distance = (distance+1)<<7 | int64(b&0x7f)
		}
		if distance <= 0 || distance > offset {
			return nil, fmt.Errorf("invalid delta base offset")
		}
		entry.isDelta = true
		entry.BaseOffset = offset - distance

	case packRefDelta:
		base := make([]byte, sha1.Size)
		if _, err := io.ReadFull(r, base); err != nil {
			return nil, err
		}
		entry.isDelta = true
		entry.BaseHash = hex.EncodeToString(base)

	default:
		return nil, fmt.Errorf("unknown object type %d", typ)
	}

	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if entry.Data, err = io.ReadAll(zr); err != nil {
		return nil, err
	}
	if uint64(len(entry.Data)) != size {
		return nil, fmt.Errorf("size mismatch: header says %d, inflated %d", size, len(entry.Data))
	}
	return entry, nil
}

// applyDelta reconstructs an object from its base and a git delta.
func applyDelta(base, delta []byte) ([]byte, error) {
	readSize := func() (uint64, error) {
		var size uint64
		for shift := uint(0); ; shift += 7 {
			if len(delta) == 0 || shift > 63 {
				return 0, fmt.Errorf("malformed delta header")
			}
			b := delta[0]
			delta = delta[1:]
			size |= uint64(b&0x7f) << shift
			if b&0x80 == 0 {
				return size, nil
			}
		}
	}

	baseSize, err := readSize()
	if err != nil {
		return nil, err
	}
	if baseSize != uint64(len(base)) {
		return nil, fmt.Errorf("delta base size mismatch: want %d, have %d", baseSize, len(base))
	}
	targetSize, err := readSize()
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, targetSize)
	for len(delta) > 0 {
		cmd := delta[0]
		delta = delta[1:]

		switch {
		case cmd&0x80 != 0:
			// Copy from base: bits 0-3 select offset bytes, 4-6 size bytes.
			var offset, size uint64
			for i := uint(0); i < 7; i++ {
				if cmd&(1<<i) == 0 {
					continue
				}
				if len(delta) == 0 {
					return nil, fmt.Errorf("truncated delta copy instruction")
				}
				if i < 4 {
					offset |= uint64(delta[0]) << (8 * i)
				} else {
					size |= uint64(delta[0]) << (8 * (i - 4))
				}
				delta = delta[1:]
			}
			if size == 0 {
				size = 0x10000
			}
			if offset+size > uint64(len(base)) {
				return nil, fmt.Errorf("delta copy out of range")
			}
			out = append(out, base[offset:offset+size]...)

		case cmd != 0:
			// Insert the next cmd bytes literally.
			n := int(cmd)
			if n > len(delta) {
				return nil, fmt.Errorf("truncated delta insert instruction")
			}
			out = append(out, delta[:n]...)
			delta = delta[n:]

		default:
			return nil, fmt.Errorf("invalid delta instruction 0")
		}
	}

	if uint64(len(out)) != targetSize {
		return nil, fmt.Errorf("delta result size mismatch: want %d, got %d", targetSize, len(out))
	}
	return out, nil
}

// unpackPack writes every object of a packfile into gitDir as a loose
// object, resolving deltas, and returns the number of objects written.
func unpackPack(gitDir string, pack []byte) (int, error) {
	entries, err := parsePack(pack)
	if err != nil {
		return 0, err
	}

	byOffset := make(map[int64]*packEntry, len(entries))
	byHash := make(map[string]*packEntry, len(entries))
	var pending []*packEntry

	store := func(e *packEntry) error {
		hash, err := writeObject(gitDir, e.Type, e.Data)
		if err != nil {
			return err
		}
		byHash[hash] = e
		return nil
	}

	for _, e := range entries {
		byOffset[e.Offset] = e
		if e.isDelta {
			pending = append(pending, e)
		} else if err := store(e); err != nil {
			return 0, err
		}
	}

	// A delta's base may itself be a delta, or appear later in the pack, so
	// keep resolving until a full pass makes no progress.
	for len(pending) > 0 {
		var unresolved []*packEntry
		for _, e := range pending {
			base := byHash[e.BaseHash]
			if e.BaseHash == "" {
				base = byOffset[e.BaseOffset]
			}
			if base == nil || base.Type == "" {
				unresolved = append(unresolved, e)
				continue
			}
			data, err := applyDelta(base.Data, e.Data)
			if err != nil {
				return 0, fmt.Errorf("pack object at offset %d: %w", e.Offset, err)
			}
			e.Type, e.Data = base.Type, data
			if err := store(e); err != nil {
				return 0, err
			}
		}
		if len(unresolved) == len(pending) {
			return 0, fmt.Errorf("%d delta objects have no base in the packfile", len(unresolved))
		}
		pending = unresolved
	}

	return len(entries), nil
}

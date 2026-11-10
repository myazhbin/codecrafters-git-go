package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// packBuilder assembles a packfile by hand so tests control exactly which
// entry types and delta encodings appear.
type packBuilder struct {
	buf   bytes.Buffer
	count uint32
}

func newPackBuilder() *packBuilder {
	b := &packBuilder{}
	b.buf.WriteString("PACK\x00\x00\x00\x02\x00\x00\x00\x00")
	return b
}

// add appends an entry and returns the offset it was written at. extra is
// written between the header and the compressed data (delta base reference).
func (b *packBuilder) add(typ int, extra, data []byte) int64 {
	offset := int64(b.buf.Len())
	size := len(data)
	first := byte(typ<<4) | byte(size&0x0f)
	size >>= 4
	for size > 0 {
		b.buf.WriteByte(first | 0x80)
		first = byte(size & 0x7f)
		size >>= 7
	}
	b.buf.WriteByte(first)
	b.buf.Write(extra)
	zw := zlib.NewWriter(&b.buf)
	zw.Write(data)
	zw.Close()
	b.count++
	return offset
}

func (b *packBuilder) addOfsDelta(baseOffset int64, delta []byte) int64 {
	distance := int64(b.buf.Len()) - baseOffset
	enc := []byte{byte(distance & 0x7f)}
	for distance >>= 7; distance > 0; distance >>= 7 {
		distance--
		enc = append([]byte{0x80 | byte(distance&0x7f)}, enc...)
	}
	return b.add(packOfsDelta, enc, delta)
}

func (b *packBuilder) addRefDelta(baseHash string, delta []byte) int64 {
	raw, _ := hex.DecodeString(baseHash)
	return b.add(packRefDelta, raw, delta)
}

func (b *packBuilder) bytes() []byte {
	out := append([]byte(nil), b.buf.Bytes()...)
	binary.BigEndian.PutUint32(out[8:12], b.count)
	sum := sha1.Sum(out)
	return append(out, sum[:]...)
}

// deltaSize encodes a size for a delta header.
func deltaSize(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		if n >>= 7; n == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

// makeDelta builds a delta from header sizes and raw instructions.
func makeDelta(baseSize, targetSize int, instructions ...[]byte) []byte {
	delta := append(deltaSize(baseSize), deltaSize(targetSize)...)
	for _, ins := range instructions {
		delta = append(delta, ins...)
	}
	return delta
}

func insert(s string) []byte { return append([]byte{byte(len(s))}, s...) }

func TestApplyDelta(t *testing.T) {
	base := []byte("hello world, this is the base object")

	// copy "hello " (offset 0, size 6), insert "there", copy " world" (offset 5, size 6)
	delta := makeDelta(len(base), 17,
		[]byte{0x80 | 0x10, 6}, // offset omitted (0), one size byte
		insert("there"),
		[]byte{0x80 | 0x01 | 0x10, 5, 6},
	)
	got, err := applyDelta(base, delta)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello there world" {
		t.Errorf("result = %q", got)
	}
}

func TestApplyDeltaLargeCopy(t *testing.T) {
	// A copy with no size bytes means 0x10000 bytes; multi-byte offsets and
	// sizes are little-endian.
	base := bytes.Repeat([]byte("0123456789abcdef"), 0x2000) // 0x20000 bytes
	delta := makeDelta(len(base), 0x10000+0x1234,
		[]byte{0x80 | 0x02, 0x01}, // offset 0x100, size 0x10000
		[]byte{0x80 | 0x01 | 0x04 | 0x10 | 0x20, 0x03, 0x01, 0x34, 0x12}, // offset 0x10003, size 0x1234
	)
	got, err := applyDelta(base, delta)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte(nil), base[0x100:0x100+0x10000]...), base[0x10003:0x10003+0x1234]...)
	if !bytes.Equal(got, want) {
		t.Error("large copy produced the wrong result")
	}
}

func TestApplyDeltaErrors(t *testing.T) {
	base := []byte("0123456789")
	for name, delta := range map[string][]byte{
		"empty":               {},
		"base size mismatch":  makeDelta(5, 1, insert("x")),
		"copy out of range":   makeDelta(10, 4, []byte{0x80 | 0x01 | 0x10, 8, 4}),
		"truncated insert":    makeDelta(10, 5, []byte{5, 'a'}),
		"truncated copy":      makeDelta(10, 5, []byte{0x80 | 0x01}),
		"reserved zero":       makeDelta(10, 1, []byte{0}),
		"target size too big": makeDelta(10, 9, insert("x")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := applyDelta(base, delta); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestUnpackPack(t *testing.T) {
	inTempRepo(t)

	// Large enough that the size needs several header bytes.
	baseContent := []byte(strings.Repeat("line of the base blob\n", 200))
	baseHash, _ := hashObject("blob", baseContent)

	v2 := append(append([]byte(nil), baseContent[:100]...), "!"...)
	v2Hash, _ := hashObject("blob", v2)
	v3 := append(append([]byte(nil), v2...), "?"...)
	v3Hash, _ := hashObject("blob", v3)
	v4 := []byte("prefix " + string(baseContent[:10]))
	v4Hash, _ := hashObject("blob", v4)

	b := newPackBuilder()
	// A ref-delta whose base only appears later in the pack.
	b.addRefDelta(baseHash, makeDelta(len(baseContent), len(v4), insert("prefix "), []byte{0x80 | 0x10, 10}))
	b.add(packCommit, nil, []byte("tree "+emptyTree+"\n\nmsg\n"))
	b.add(packTree, nil, nil)
	baseOff := b.add(packBlob, nil, baseContent)
	v2Off := b.addOfsDelta(baseOff, makeDelta(len(baseContent), len(v2), []byte{0x80 | 0x10, 100}, insert("!")))
	// A delta on top of another delta.
	b.addOfsDelta(v2Off, makeDelta(len(v2), len(v3), []byte{0x80 | 0x10, 101}, insert("?")))
	b.add(packTag, nil, []byte("object "+baseHash+"\ntype blob\ntag v1\n\nmsg\n"))

	n, err := unpackPack(".git", b.bytes())
	if err != nil {
		t.Fatalf("unpackPack: %v", err)
	}
	if n != 7 {
		t.Errorf("unpacked %d objects, want 7", n)
	}

	for hash, want := range map[string][]byte{baseHash: baseContent, v2Hash: v2, v3Hash: v3, v4Hash: v4} {
		typ, got, err := readObject(".git", hash)
		if err != nil {
			t.Fatalf("object %s: %v", hash, err)
		}
		if typ != "blob" || !bytes.Equal(got, want) {
			t.Errorf("object %s = (%s, %d bytes), want blob of %d bytes", hash, typ, len(got), len(want))
		}
	}
	if typ, _, err := readObject(".git", emptyTree); err != nil || typ != "tree" {
		t.Errorf("empty tree not unpacked: %v", err)
	}
	tagHash, _ := hashObject("tag", []byte("object "+baseHash+"\ntype blob\ntag v1\n\nmsg\n"))
	if typ, _, err := readObject(".git", tagHash); err != nil || typ != "tag" {
		t.Errorf("tag not unpacked: %v", err)
	}
}

func TestUnpackPackErrors(t *testing.T) {
	inTempRepo(t)

	good := newPackBuilder()
	good.add(packBlob, nil, []byte("hello"))
	pack := good.bytes()

	corrupt := append([]byte(nil), pack...)
	corrupt[len(corrupt)-1] ^= 0xff

	badVersion := append([]byte(nil), pack[:len(pack)-20]...)
	badVersion[7] = 3
	sum := sha1.Sum(badVersion)
	badVersion = append(badVersion, sum[:]...)

	orphan := newPackBuilder()
	orphan.addRefDelta(zeroHash, makeDelta(0, 1, insert("x")))

	for name, data := range map[string][]byte{
		"too short":     []byte("PACK"),
		"bad signature": append([]byte("JUNK"), pack[4:]...),
		"bad checksum":  corrupt,
		"bad version":   badVersion,
		"missing base":  orphan.bytes(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := unpackPack(".git", data); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

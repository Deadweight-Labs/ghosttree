package snapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"sort"
)

var contentDigestPrefix = []byte("ghosttree-context-snapshot\x00")

func EntryDigest(payload []byte) Digest { return sha256.Sum256(payload) }

func ContentDigest(schemaVersion uint32, entries []EntrySummary) Digest {
	ordered := append([]EntrySummary(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Domain != ordered[j].Domain {
			return ordered[i].Domain < ordered[j].Domain
		}
		return ordered[i].Key < ordered[j].Key
	})

	digest := sha256.New()
	digest.Write(contentDigestPrefix)
	writeUint32(digest, schemaVersion)
	for _, entry := range ordered {
		writeFramedString(digest, entry.Domain)
		writeFramedString(digest, entry.Key)
		digest.Write(entry.PayloadDigest[:])
	}
	var result Digest
	copy(result[:], digest.Sum(nil))
	return result
}

func LogicalSize(canonicalHead []byte, entries []EntrySummary) int64 {
	total := int64(len(canonicalHead))
	for _, entry := range entries {
		total += int64(len(entry.Domain)) + int64(len(entry.Key)) + int64(len(entry.PayloadDigest)) + entry.PayloadSize
	}
	return total
}

func writeUint32(dst hash.Hash, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	dst.Write(encoded[:])
}

func writeFramedString(dst hash.Hash, value string) {
	writeUint32(dst, uint32(len(value)))
	dst.Write([]byte(value))
}

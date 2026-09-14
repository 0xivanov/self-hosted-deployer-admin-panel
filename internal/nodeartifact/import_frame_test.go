package nodeartifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func exportFrame(t *testing.T, archive []byte, executionID, sourceSHA256, snapshotSHA256 string) []byte {
	t.Helper()
	archiveSum := sha256.Sum256(archive)
	header, err := json.Marshal(map[string]any{
		"Version": 1, "ExecutionID": executionID, "SourceSHA256": sourceSHA256,
		"SnapshotSHA256": snapshotSHA256, "ArchiveSHA256": hex.EncodeToString(archiveSum[:]), "ArchiveBytes": int64(len(archive)),
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, exportFrameHeaderSize+len(archive))
	copy(frame, header)
	copy(frame[exportFrameHeaderSize:], archive)
	return frame
}

func TestReadExportValidFrame(t *testing.T) {
	t.Parallel()
	archive, archiveSHA := fixture(t)
	executionID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sourceSHA := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	snapshotSHA := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	data, manifest, err := ReadExport(t.Context(), bytes.NewReader(exportFrame(t, archive, executionID, sourceSHA, snapshotSHA)), 64<<20, executionID, sourceSHA, snapshotSHA)
	if err != nil || !bytes.Equal(data, archive) || manifest.SHA256 != archiveSHA {
		t.Fatal(manifest, err)
	}
}

func TestReadExportRejectsInvalidFrames(t *testing.T) {
	t.Parallel()
	archive, _ := fixture(t)
	executionID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sourceSHA := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	snapshotSHA := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	badPadding := exportFrame(t, archive, executionID, sourceSHA, snapshotSHA)
	badPadding[exportFrameHeaderSize-1] = 1
	badDigest := exportFrame(t, archive, executionID, sourceSHA, snapshotSHA)
	badDigest[len(badDigest)-1] ^= 1
	unknown := exportFrame(t, archive, executionID, sourceSHA, snapshotSHA)
	end := bytes.IndexByte(unknown, 0)
	copy(unknown[end-1:], []byte(",\"Unknown\":1}"))
	cases := []struct {
		name string
		data []byte
		size int64
	}{
		{"nonzero padding", badPadding, 64 << 20},
		{"wrong archive digest", badDigest, 64 << 20},
		{"unknown header field", unknown, 64 << 20},
		{"oversized disk", exportFrame(t, archive, executionID, sourceSHA, snapshotSHA), 513 << 20},
		{"wrong identity", exportFrame(t, archive, executionID, sourceSHA, snapshotSHA), 64 << 20},
		{"truncated", exportFrame(t, archive, executionID, sourceSHA, snapshotSHA)[:exportFrameHeaderSize+len(archive)-1], 64 << 20},
		{"undersized disk", exportFrame(t, archive, executionID, sourceSHA, snapshotSHA), 63 << 20},
		{"unsafe ZIP", exportFrame(t, mustUnsafeArchive(t), executionID, sourceSHA, snapshotSHA), 64 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantExecution := executionID
			if tc.name == "wrong identity" {
				wantExecution = strings.Repeat("a", 64)
			}
			if _, _, err := ReadExport(t.Context(), bytes.NewReader(tc.data), tc.size, wantExecution, sourceSHA, snapshotSHA); err == nil {
				t.Fatal("invalid export accepted")
			}
		})
	}
}

func mustUnsafeArchive(t *testing.T) []byte {
	t.Helper()
	data, _ := fixture(t, fixtureEntry{"../escape", "x", 0600})
	return data
}

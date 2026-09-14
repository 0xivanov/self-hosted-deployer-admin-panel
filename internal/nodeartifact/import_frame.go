package nodeartifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const exportFrameHeaderSize = 4096

type exportFrameHeader struct {
	Version        *int    `json:"Version"`
	ExecutionID    *string `json:"ExecutionID"`
	SourceSHA256   *string `json:"SourceSHA256"`
	SnapshotSHA256 *string `json:"SnapshotSHA256"`
	ArchiveSHA256  *string `json:"ArchiveSHA256"`
	ArchiveBytes   *int64  `json:"ArchiveBytes"`
}

// ReadExport validates and reads an exporter's bounded raw disk frame. The
// caller is responsible for proving that the exporter VM stopped and that the
// disk was assigned to this operation before calling this function.
func ReadExport(ctx context.Context, r io.ReaderAt, size int64, executionID, sourceSHA256, snapshotSHA256 string) ([]byte, Manifest, error) {
	if r == nil || size < 64<<20 || size > 512<<20 || size%512 != 0 || !validDigest(executionID) || !validDigest(sourceSHA256) || !validDigest(snapshotSHA256) {
		return nil, Manifest{}, ErrArtifact
	}
	if err := ctx.Err(); err != nil {
		return nil, Manifest{}, err
	}
	headerBytes := make([]byte, exportFrameHeaderSize)
	if _, err := io.ReadFull(io.NewSectionReader(r, 0, exportFrameHeaderSize), headerBytes); err != nil {
		return nil, Manifest{}, ErrArtifact
	}
	padding := bytes.IndexByte(headerBytes, 0)
	if padding < 0 {
		return nil, Manifest{}, ErrArtifact
	}
	if bytes.IndexFunc(headerBytes[padding:], func(r rune) bool { return r != 0 }) >= 0 {
		return nil, Manifest{}, ErrArtifact
	}
	decoder := json.NewDecoder(bytes.NewReader(headerBytes[:padding]))
	decoder.DisallowUnknownFields()
	var header exportFrameHeader
	if err := decoder.Decode(&header); err != nil {
		return nil, Manifest{}, ErrArtifact
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, Manifest{}, ErrArtifact
	}
	if header.Version == nil || *header.Version != 1 || header.ExecutionID == nil || header.SourceSHA256 == nil || header.SnapshotSHA256 == nil || header.ArchiveSHA256 == nil || header.ArchiveBytes == nil ||
		!validDigest(*header.ExecutionID) || !validDigest(*header.SourceSHA256) || !validDigest(*header.SnapshotSHA256) || !validDigest(*header.ArchiveSHA256) ||
		*header.ExecutionID != executionID || *header.SourceSHA256 != sourceSHA256 || *header.SnapshotSHA256 != snapshotSHA256 || *header.ArchiveBytes < 1 || *header.ArchiveBytes > MaxCompressed || *header.ArchiveBytes > size-exportFrameHeaderSize {
		return nil, Manifest{}, ErrArtifact
	}
	if err := ctx.Err(); err != nil {
		return nil, Manifest{}, err
	}
	archive := make([]byte, int(*header.ArchiveBytes))
	if _, err := io.ReadFull(io.NewSectionReader(r, exportFrameHeaderSize, *header.ArchiveBytes), archive); err != nil {
		return nil, Manifest{}, ErrArtifact
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != *header.ArchiveSHA256 {
		return nil, Manifest{}, ErrArtifact
	}
	manifest, err := Validate(ctx, archive, *header.ArchiveSHA256)
	if err != nil {
		return nil, Manifest{}, err
	}
	return archive, manifest, nil
}

func validDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

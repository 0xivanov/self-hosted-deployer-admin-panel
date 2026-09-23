package registryimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const ManifestLimit = 2 << 20
const ConfigLimit = 1 << 20
const MaxLayerBytes = int64(512 << 20)
const ociIndex = "application/vnd.oci.image.index.v1+json"
const dockerIndex = "application/vnd.docker.distribution.manifest.list.v2+json"
const ociManifest = "application/vnd.oci.image.manifest.v1+json"
const dockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
const Accept = ociIndex + ", " + dockerIndex + ", " + ociManifest + ", " + dockerManifest

var ErrManifest = errors.New("registry image metadata is invalid or unsupported")
var ErrPlatform = errors.New("image must provide exactly one compatible linux/arm64 variant")
var ErrSize = errors.New("image exceeds the 512 MiB compressed-layer or metadata limits")

type descriptor struct {
	MediaType string   `json:"mediaType"`
	Digest    string   `json:"digest"`
	Size      int64    `json:"size"`
	URLs      []string `json:"urls"`
	Platform  struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	ArtifactType  string       `json:"artifactType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
	Manifests     []descriptor `json:"manifests"`
}

// Fetcher returns bounded raw registry bytes for one already-validated reference.
// Implementations must keep credentials private and restrict network destinations.
type Fetcher interface {
	Manifest(context.Context, string) ([]byte, error)
	Config(context.Context, string) ([]byte, error)
}
type Candidate struct {
	Source         string `json:"source"`
	SourceDigest   string `json:"source_digest"`
	Image          string `json:"image"`
	ManifestDigest string `json:"manifest_digest"`
	ConfigDigest   string `json:"config_digest"`
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	LayerBytes     int64  `json:"compressed_layer_bytes"`
	ExposedPorts   []int  `json:"exposed_tcp_ports"`
}

func hash(b []byte) string { sum := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(sum[:]) }
func verified(b []byte, d descriptor) bool {
	return d.Size >= 0 && int64(len(b)) == d.Size && digestPattern.MatchString(d.Digest) && hash(b) == d.Digest && len(d.URLs) == 0
}
func compatible(os, arch, variant string) bool {
	return os == "linux" && arch == "arm64" && (variant == "" || variant == "v8")
}

func Validate(ctx context.Context, r Reference, f Fetcher) (Candidate, error) {
	var out Candidate
	canonical, err := Parse(r.String())
	if err != nil || canonical != r || f == nil {
		return out, ErrReference
	}
	raw, err := f.Manifest(ctx, r.Version)
	if err != nil {
		return out, err
	}
	if len(raw) > ManifestLimit {
		return out, ErrSize
	}
	sourceHash := hash(raw)
	if digestPattern.MatchString(r.Version) && sourceHash != r.Version {
		return out, ErrManifest
	}
	var m manifest
	if json.Unmarshal(raw, &m) != nil || m.SchemaVersion != 2 || m.ArtifactType != "" {
		return out, ErrManifest
	}
	if m.MediaType == ociIndex || m.MediaType == dockerIndex {
		var selected []descriptor
		for _, d := range m.Manifests {
			if compatible(d.Platform.OS, d.Platform.Architecture, d.Platform.Variant) {
				selected = append(selected, d)
			}
		}
		if len(selected) != 1 {
			return out, ErrPlatform
		}
		d := selected[0]
		if !digestPattern.MatchString(d.Digest) || d.Size <= 0 || d.Size > ManifestLimit || len(d.URLs) > 0 || (d.MediaType != ociManifest && d.MediaType != dockerManifest) {
			return out, ErrManifest
		}
		raw, err = f.Manifest(ctx, d.Digest)
		if err != nil {
			return out, err
		}
		if !verified(raw, d) {
			return out, ErrManifest
		}
		m = manifest{}
		if json.Unmarshal(raw, &m) != nil || m.MediaType != d.MediaType {
			return out, ErrManifest
		}
	}
	if m.SchemaVersion != 2 || m.ArtifactType != "" || (m.MediaType != ociManifest && m.MediaType != dockerManifest) || len(m.Layers) > 128 || len(m.Manifests) > 0 {
		return out, ErrManifest
	}
	if !digestPattern.MatchString(m.Config.Digest) || m.Config.Size <= 0 || m.Config.Size > ConfigLimit || len(m.Config.URLs) > 0 || (m.Config.MediaType != "application/vnd.oci.image.config.v1+json" && m.Config.MediaType != "application/vnd.docker.container.image.v1+json") {
		return out, ErrManifest
	}
	var total int64
	for _, d := range m.Layers {
		if !digestPattern.MatchString(d.Digest) || d.Size < 0 || len(d.URLs) > 0 {
			return out, ErrManifest
		}
		switch d.MediaType {
		case "application/vnd.oci.image.layer.v1.tar", "application/vnd.oci.image.layer.v1.tar+gzip", "application/vnd.oci.image.layer.v1.tar+zstd", "application/vnd.docker.image.rootfs.diff.tar.gzip":
		default:
			return out, ErrManifest
		}
		if d.Size > MaxLayerBytes-total {
			return out, ErrSize
		}
		total += d.Size
	}
	config, err := f.Config(ctx, m.Config.Digest)
	if err != nil {
		return out, err
	}
	if !verified(config, m.Config) {
		return out, ErrManifest
	}
	var c struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
		Config       struct {
			ExposedPorts map[string]json.RawMessage `json:"ExposedPorts"`
		} `json:"config"`
	}
	if json.Unmarshal(config, &c) != nil {
		return out, ErrManifest
	}
	if !compatible(c.OS, c.Architecture, c.Variant) {
		return out, ErrPlatform
	}
	ports := []int{}
	for p := range c.Config.ExposedPorts {
		if strings.HasSuffix(p, "/tcp") {
			if n, e := strconv.Atoi(strings.TrimSuffix(p, "/tcp")); e == nil && n > 0 && n <= 65535 {
				ports = append(ports, n)
			}
		}
	}
	sort.Ints(ports)
	pinned := hash(raw)
	return Candidate{Source: r.String(), SourceDigest: sourceHash, Image: fmt.Sprintf("%s/%s@%s", r.Registry, r.Repository, pinned), ManifestDigest: pinned, ConfigDigest: m.Config.Digest, OS: c.OS, Architecture: c.Architecture, LayerBytes: total, ExposedPorts: ports}, nil
}

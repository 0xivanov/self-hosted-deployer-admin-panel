package registryimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeFetcher struct{ manifests, configs map[string][]byte }

func (f fakeFetcher) Manifest(_ context.Context, v string) ([]byte, error) {
	b, ok := f.manifests[v]
	if !ok {
		return nil, errors.New("missing manifest")
	}
	return b, nil
}
func (f fakeFetcher) Config(_ context.Context, v string) ([]byte, error) {
	b, ok := f.configs[v]
	if !ok {
		return nil, errors.New("missing config")
	}
	return b, nil
}

func digestBytes(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
func descriptorFor(media, d string, b []byte) descriptor {
	return descriptor{MediaType: media, Digest: d, Size: int64(len(b))}
}

func validImageFixture(t *testing.T, index bool) (Reference, fakeFetcher, string, string) {
	t.Helper()
	config := []byte(`{"os":"linux","architecture":"arm64","variant":"v8","config":{"ExposedPorts":{"8080/tcp":{}}}}`)
	cd := digestBytes(config)
	layer := []byte("layer")
	ld := digestBytes(layer)
	child := map[string]any{"schemaVersion": 2, "mediaType": ociManifest, "config": descriptorFor("application/vnd.oci.image.config.v1+json", cd, config), "layers": []descriptor{descriptorFor("application/vnd.oci.image.layer.v1.tar+gzip", ld, layer)}}
	childRaw, _ := json.Marshal(child)
	childDigest := digestBytes(childRaw)
	var sourceRaw []byte
	if index {
		source := map[string]any{"schemaVersion": 2, "mediaType": ociIndex, "manifests": []descriptor{{MediaType: ociManifest, Digest: childDigest, Size: int64(len(childRaw)), Platform: struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
		}{OS: "linux", Architecture: "arm64", Variant: "v8"}}}}
		sourceRaw, _ = json.Marshal(source)
	} else {
		sourceRaw = childRaw
	}
	sourceDigest := digestBytes(sourceRaw)
	r, _ := Parse("ghcr.io/acme/demo@" + sourceDigest)
	f := fakeFetcher{manifests: map[string][]byte{r.Version: sourceRaw, childDigest: childRaw}, configs: map[string][]byte{cd: config}}
	return r, f, childDigest, cd
}

func TestValidateResolvesArm64IndexAndVerifiesConfigAndChild(t *testing.T) {
	r, f, child, config := validImageFixture(t, true)
	c, err := Validate(context.Background(), r, f)
	if err != nil {
		t.Fatal(err)
	}
	if c.ManifestDigest != child || c.ConfigDigest != config || c.Architecture != "arm64" || c.OS != "linux" {
		t.Fatalf("unexpected candidate: %#v", c)
	}
	if c.Image != "ghcr.io/acme/demo@"+child {
		t.Fatalf("unexpected pinned image %q", c.Image)
	}
}

func TestValidateRejectsDigestTamperingAndAmbiguousOrForeignMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Reference, fakeFetcher, string) (Reference, fakeFetcher)
	}{
		{"source digest", func(r Reference, x fakeFetcher, child string) (Reference, fakeFetcher) {
			x.manifests[r.Version] = append(x.manifests[r.Version], 'x')
			return r, x
		}},
		{"child digest", func(r Reference, x fakeFetcher, child string) (Reference, fakeFetcher) {
			x.manifests[child] = append(x.manifests[child], 'x')
			return r, x
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, f, child, _ := validImageFixture(t, true)
			r, f = tt.mutate(r, f, child)
			if _, err := Validate(context.Background(), r, f); !errors.Is(err, ErrManifest) {
				t.Fatalf("got %v, want %v", err, ErrManifest)
			}
		})
	}

	r2, f2, _, _ := validImageFixture(t, true)
	r2Original := r2.Version
	r2, _ = Parse("ghcr.io/acme/demo:tag")
	f2.manifests[r2.Version] = f2.manifests[r2Original]
	var idx map[string]any
	_ = json.Unmarshal(f2.manifests[r2.Version], &idx)
	idx["manifests"] = []any{}
	// Empty indexes are rejected as ambiguous because no compatible variant exists.
	f2.manifests[r2.Version], _ = json.Marshal(idx)
	if _, err := Validate(context.Background(), r2, f2); !errors.Is(err, ErrPlatform) {
		t.Fatalf("empty index error = %v", err)
	}
	// Two compatible variants are ambiguous and must not be auto-selected.
	r4, f4, child4, _ := validImageFixture(t, true)
	r4Original := r4.Version
	r4, _ = Parse("ghcr.io/acme/demo:tag")
	f4.manifests[r4.Version] = f4.manifests[r4Original]
	var idx4 map[string]any
	_ = json.Unmarshal(f4.manifests[r4.Version], &idx4)
	entries := idx4["manifests"].([]any)
	idx4["manifests"] = append(entries, entries[0])
	f4.manifests[r4.Version], _ = json.Marshal(idx4)
	_ = child4
	if _, err := Validate(context.Background(), r4, f4); !errors.Is(err, ErrPlatform) {
		t.Fatalf("ambiguous index error = %v", err)
	}

	r3, f3, _, _ := validImageFixture(t, false)
	r3Original := r3.Version
	r3, _ = Parse("ghcr.io/acme/demo:tag")
	f3.manifests[r3.Version] = f3.manifests[r3Original]
	var man map[string]any
	_ = json.Unmarshal(f3.manifests[r3.Version], &man)
	man["artifactType"] = "application/vnd.example.artifact"
	f3.manifests[r3.Version], _ = json.Marshal(man)
	if _, err := Validate(context.Background(), r3, f3); !errors.Is(err, ErrManifest) {
		t.Fatalf("artifact error = %v", err)
	}
}

func TestValidateRejectsConfigArchitectureMismatchAndOversizedMetadata(t *testing.T) {
	r, f, _, cd := validImageFixture(t, false)
	rOriginal := r.Version
	r, _ = Parse("ghcr.io/acme/demo:tag")
	f.manifests[r.Version] = f.manifests[rOriginal]
	bad := []byte(`{"os":"linux","architecture":"amd64","config":{}}`)
	badDigest := digestBytes(bad)
	f.configs[badDigest] = bad
	var man map[string]any
	_ = json.Unmarshal(f.manifests[r.Version], &man)
	man["config"] = map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": badDigest, "size": len(bad)}
	f.manifests[r.Version], _ = json.Marshal(man)
	delete(f.configs, cd)
	if _, err := Validate(context.Background(), r, f); !errors.Is(err, ErrPlatform) {
		t.Fatalf("architecture mismatch error = %v", err)
	}

	r, f, _, _ = validImageFixture(t, false)
	original := r.Version
	r, _ = Parse("ghcr.io/acme/demo:tag")
	f.manifests[r.Version] = f.manifests[original]
	f.manifests[r.Version] = []byte(strings.Repeat("x", ManifestLimit+1))
	if _, err := Validate(context.Background(), r, f); !errors.Is(err, ErrSize) {
		t.Fatalf("oversized manifest error = %v", err)
	}
}

func TestValidateRejectsLayerLimitAndForeignURLs(t *testing.T) {
	r, f, _, _ := validImageFixture(t, false)
	original := r.Version
	r, _ = Parse("ghcr.io/acme/demo:tag")
	f.manifests[r.Version] = f.manifests[original]
	var m map[string]any
	_ = json.Unmarshal(f.manifests[r.Version], &m)
	m["layers"] = []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + strings.Repeat("a", 64), "size": MaxLayerBytes + 1}}
	f.manifests[r.Version], _ = json.Marshal(m)
	if _, err := Validate(context.Background(), r, f); !errors.Is(err, ErrSize) {
		t.Fatalf("layer size error = %v", err)
	}

	r, f, _, _ = validImageFixture(t, false)
	original = r.Version
	r, _ = Parse("ghcr.io/acme/demo:tag")
	f.manifests[r.Version] = f.manifests[original]
	_ = json.Unmarshal(f.manifests[r.Version], &m)
	m["config"] = map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + strings.Repeat("a", 64), "size": 1, "urls": []string{"https://foreign.example/config"}}
	f.manifests[r.Version], _ = json.Marshal(m)
	if _, err := Validate(context.Background(), r, f); !errors.Is(err, ErrManifest) {
		t.Fatalf("foreign URL error = %v", err)
	}
}

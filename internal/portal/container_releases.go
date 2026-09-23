package portal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

var ErrContainerUnavailable = errors.New("container hosting is not enabled")

// ConfigureContainerProjects is startup-only. Leave disabled until every fleet
// component supports container provisioning, publication and rollback.
func (s *Store) ConfigureContainerProjects(enabled bool) { s.containerProjects = enabled }

type ContainerReleaseInput struct {
	EnvironmentID string `json:"environment_id,omitempty"`
	CredentialID  string `json:"credential_id,omitempty"`
	Reference     string `json:"reference"`
	Port          int    `json:"port"`
	HealthPath    string `json:"health_path"`
}
type ContainerRelease struct {
	ID        string                  `json:"id"`
	ProjectID string                  `json:"project_id"`
	Revision  int64                   `json:"revision"`
	Input     ContainerReleaseInput   `json:"input"`
	Image     registryimage.Candidate `json:"image"`
	CreatedAt int64                   `json:"created_at"`
}

// ContainerImageResolver is trusted operator code. Credentials must be selected
// for this project and never returned in the candidate or persisted release.
type ContainerImageResolver func(context.Context, string, string) (registryimage.Candidate, error)

func normalizeContainerInput(in ContainerReleaseInput) (ContainerReleaseInput, error) {
	for _, id := range []string{in.CredentialID, in.EnvironmentID} {
		if id != "" {
			b, err := hex.DecodeString(id)
			if err != nil || len(b) != 32 || hex.EncodeToString(b) != id {
				return in, ErrInvalid
			}
		}
	}
	ref, err := registryimage.Parse(in.Reference)
	if err != nil {
		return in, err
	}
	in.Reference = ref.String()
	u, err := url.ParseRequestURI(in.HealthPath)
	if err != nil || !strings.HasPrefix(in.HealthPath, "/") || strings.HasPrefix(in.HealthPath, "//") || len(in.HealthPath) > 512 || u.Host != "" || u.Scheme != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(in.HealthPath, "?#\r\n\x00") || in.Port < 1024 || in.Port > 65535 {
		return in, ErrInvalid
	}
	for _, c := range u.Path {
		if c < 32 || c == 127 {
			return in, ErrInvalid
		}
	}
	return in, nil
}
func validContainerCandidate(in ContainerReleaseInput, c registryimage.Candidate) bool {
	source, e := registryimage.Parse(in.Reference)
	if e != nil {
		return false
	}
	pin, e := registryimage.Parse(c.Image)
	if e != nil {
		return false
	}
	if c.Source != in.Reference || pin.String() != c.Image || pin.Registry != source.Registry || pin.Repository != source.Repository || !strings.HasPrefix(pin.Version, "sha256:") || pin.Version != c.ManifestDigest || c.OS != "linux" || c.Architecture != "arm64" || c.LayerBytes < 0 || c.LayerBytes > registryimage.MaxLayerBytes {
		return false
	}
	for _, d := range []string{c.SourceDigest, c.ConfigDigest} {
		if _, e := registryimage.Parse(source.Registry + "/" + source.Repository + "@" + d); e != nil {
			return false
		}
	}
	if strings.HasPrefix(source.Version, "sha256:") && source.Version != c.SourceDigest {
		return false
	}
	return true
}
func savedContainerRelease(ctx context.Context, tx *sql.Tx, project, key string) (ContainerRelease, error) {
	var r ContainerRelease
	var input, image []byte
	err := tx.QueryRowContext(ctx, "SELECT id,project_id,revision,input,image,created_at FROM container_releases WHERE project_id=? AND request_key=?", project, key).Scan(&r.ID, &r.ProjectID, &r.Revision, &input, &image, &r.CreatedAt)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(input, &r.Input); err != nil {
		return r, err
	}
	err = json.Unmarshal(image, &r.Image)
	return r, err
}
func (s *Store) containerAccess(ctx context.Context, tx *sql.Tx, token, project string) (Project, string, error) {
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return p, actor, err
	}
	if p.Kind != "container" {
		return p, actor, ErrInvalid
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return p, actor, err
	}
	return p, actor, nil
}

// PrepareContainerRelease saves metadata only. A retry keeps the original pin
// even when a registry tag has moved. No database lock spans the registry call.
func (s *Store) PrepareContainerRelease(ctx context.Context, token, project, key string, in ContainerReleaseInput, resolve ContainerImageResolver) (ContainerRelease, error) {
	var zero ContainerRelease
	if !s.containerProjects {
		return zero, ErrContainerUnavailable
	}
	if len(key) < 16 || len(key) > 128 || resolve == nil {
		return zero, ErrInvalid
	}
	in, err := normalizeContainerInput(in)
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, _, err = s.containerAccess(ctx, tx, token, project); err != nil {
		return zero, err
	}
	if err = validateContainerEnvironmentReference(ctx, tx, project, in.EnvironmentID); err != nil {
		return zero, err
	}
	old, err := savedContainerRelease(ctx, tx, project, key)
	if err == nil {
		if old.Input != in {
			return zero, ErrConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	candidate, err := resolve(call, project, in.Reference)
	if err != nil {
		return zero, err
	}
	if err = call.Err(); err != nil {
		return zero, err
	}
	if !validContainerCandidate(in, candidate) {
		return zero, ErrInvalid
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	p, actor, err := s.containerAccess(ctx, tx, token, project)
	if err != nil {
		return zero, err
	}
	if err = validateContainerEnvironmentReference(ctx, tx, project, in.EnvironmentID); err != nil {
		return zero, err
	}
	old, err = savedContainerRelease(ctx, tx, project, key)
	if err == nil {
		if old.Input != in {
			return zero, ErrConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var count int
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(max(revision),0)+1 FROM container_releases WHERE project_id=?", project).Scan(&count, &revision); err != nil {
		return zero, err
	}
	if count >= 50 {
		return zero, ErrQuota
	}
	r := ContainerRelease{ID: randomToken(), ProjectID: project, Revision: revision, Input: in, Image: candidate, CreatedAt: s.now().Unix()}
	rawInput, err := json.Marshal(in)
	if err != nil {
		return zero, err
	}
	rawImage, err := json.Marshal(candidate)
	if err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO container_releases(id,project_id,actor_id,request_key,revision,input,image,created_at) VALUES(?,?,?,?,?,?,?,?)", r.ID, project, actor, key, revision, rawInput, rawImage, r.CreatedAt); err != nil {
		return zero, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "container.prepared:"+r.ID, r.CreatedAt); err != nil {
		return zero, err
	}
	return r, tx.Commit()
}

// ContainerReleases is owner/developer-only, like source and runtime logs.
// Read access remains available after plan expiry so owners can inspect history.
func (s *Store) ContainerReleases(ctx context.Context, token, project string) ([]ContainerRelease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, _, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return nil, err
	}
	if p.Kind != "container" {
		return nil, ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,project_id,revision,input,image,created_at FROM container_releases WHERE project_id=? ORDER BY revision DESC", project)
	if err != nil {
		return nil, err
	}
	out := []ContainerRelease{}
	for rows.Next() {
		var r ContainerRelease
		var input, image []byte
		if err = rows.Scan(&r.ID, &r.ProjectID, &r.Revision, &input, &image, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(input, &r.Input); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(image, &r.Image); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Resolve ownership inside both authorization transactions so metadata lookups
// and persisted releases cannot bind another project's environment revision.
func validateContainerEnvironmentReference(ctx context.Context, tx *sql.Tx, project, id string) error {
	if id == "" {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM container_environments WHERE project_id=? AND id=?", project, id).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrDenied
	}
	return nil
}

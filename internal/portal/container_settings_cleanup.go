package portal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

var ErrContainerSettingsInUse = errors.New("container settings retained by a release")

// Retain inputs for every saved release, including unpublished releases and
// rollback choices. Malformed history fails closed rather than losing secrets.
func containerSettingsReferences(ctx context.Context, tx *sql.Tx, project string) (map[string]bool, map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT input FROM container_releases WHERE project_id=?", project)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	credentials, environments := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var raw []byte
		var input ContainerReleaseInput
		if err = rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		if err = json.Unmarshal(raw, &input); err != nil {
			return nil, nil, err
		}
		if _, err = normalizeContainerInput(input); err != nil {
			return nil, nil, err
		}
		credentials[input.CredentialID] = true
		environments[input.EnvironmentID] = true
	}
	return credentials, environments, rows.Err()
}

func validateContainerCredentialReference(ctx context.Context, tx *sql.Tx, project, id, reference string) error {
	if id == "" {
		return nil
	}
	ref, err := registryimage.Parse(reference)
	if err != nil {
		return ErrInvalid
	}
	var registry string
	err = tx.QueryRowContext(ctx, "SELECT registry FROM container_credentials WHERE project_id=? AND id=?", project, id).Scan(&registry)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && registry != ref.Registry) {
		return ErrInvalid
	}
	return err
}

func (s *Store) deleteContainerSettings(ctx context.Context, token, project, id string, environment bool) error {
	if !s.containerProjects {
		return ErrContainerUnavailable
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != id {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Cleanup remains available after hosting entitlement expires.
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return err
	}
	if p.Kind != "container" {
		return ErrInvalid
	}
	credentials, environments, err := containerSettingsReferences(ctx, tx, project)
	if err != nil {
		return err
	}
	table, action, referenced := "container_credentials", "container.credentials.deleted:", credentials[id]
	if environment {
		table, action, referenced = "container_environments", "container.environments.deleted:", environments[id]
	}
	if referenced {
		return ErrContainerSettingsInUse
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE project_id=? AND id=?", project, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		if err = audit(ctx, tx, actor, p.WorkspaceID, action+id, s.now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *ContainerCredentials) Delete(ctx context.Context, token, project, id string) error {
	return c.store.deleteContainerSettings(ctx, token, project, id, false)
}

func (e *ContainerEnvironments) Delete(ctx context.Context, token, project, id string) error {
	return e.store.deleteContainerSettings(ctx, token, project, id, true)
}

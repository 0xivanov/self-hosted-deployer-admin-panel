package portal

import "context"

type ClientAccessEvent struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Actor     string `json:"actor"`
	Client    string `json:"client"`
	CreatedAt int64  `json:"created_at"`
}

// ClientAccessHistory is owner-only and projects a fixed allowlist of client
// access events. It never exposes raw audit actions, invitation tokens or events
// for other websites/workspaces.
func (s *Store) ClientAccessHistory(ctx context.Context, session, project string) ([]ClientAccessEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, _, err := s.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH kinds(prefix,action,target) AS (VALUES
 ('project.client-granted:'||?||':','granted','user'),
 ('project.client-revoked:'||?||':','revoked','user'),
 ('project.client-invited:'||?||':','invited','invitation'),
 ('project.client-invite-revoked:'||?||':','invitation_revoked','invitation'),
 ('project.client-invite-accepted:'||?||':','invitation_accepted','invitation'))
 SELECT a.id,k.action,COALESCE(actor.email,''),COALESCE(client.email,inv.email,''),a.created_at
 FROM audit_events a JOIN kinds k ON substr(a.action,1,length(k.prefix))=k.prefix
 LEFT JOIN users actor ON actor.id=a.actor_id
 LEFT JOIN users client ON k.target='user' AND client.id=substr(a.action,length(k.prefix)+1)
 LEFT JOIN client_invitations inv ON k.target='invitation' AND inv.id=substr(a.action,length(k.prefix)+1) AND inv.project_id=?
 WHERE a.workspace_id=? ORDER BY a.created_at DESC,a.rowid DESC LIMIT 50`, project, project, project, project, project, project, p.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ClientAccessEvent{}
	for rows.Next() {
		var event ClientAccessEvent
		if err = rows.Scan(&event.ID, &event.Action, &event.Actor, &event.Client, &event.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

package portal

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
)

const customDomainTarget = "159.195.146.26"

var ErrDomainLimit = errors.New("custom domain limit reached")
var ErrDomainDNS = errors.New("custom domain DNS records do not match")

type CustomDomain struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Hostname  string `json:"hostname"`
	Token     string `json:"-"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// CustomDomainWriteAccess performs the mutation authorization before any DNS lookup.
func (s *Store) CustomDomainWriteAccess(ctx context.Context, token, project, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workspace string
	if err = tx.QueryRowContext(ctx, "SELECT p.workspace_id FROM project_domains d JOIN projects p ON p.id=d.project_id WHERE d.id=? AND d.project_id=?", id, project).Scan(&workspace); errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	} else if err != nil {
		return err
	}
	if _, err = s.authorize(ctx, tx, token, workspace, true); err != nil {
		return err
	}
	var deleting int
	if err = tx.QueryRowContext(ctx, "SELECT deletion_requested_at<>0 FROM projects WHERE id=?", project).Scan(&deleting); err != nil {
		return err
	}
	if deleting != 0 {
		return ErrProjectDeleting
	}
	return tx.Commit()
}

func customDomainJSON(d CustomDomain) map[string]any {
	return map[string]any{"id": d.ID, "project": d.Project, "hostname": d.Hostname, "state": d.State, "message": d.Message, "created_at": d.CreatedAt, "dns": map[string]string{"type": "TXT", "name": "_launchstead." + d.Hostname, "value": "launchstead-verification=" + d.Token}, "a_records": []string{customDomainTarget}, "aaaa_records": []string{}}
}

func validCustomHostname(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	reserved := map[string]bool{"0xivanov.dev": true, "admin.0xivanov.dev": true, "portal.0xivanov.dev": true, "deploy.0xivanov.dev": true, "money.0xivanov.dev": true, "sslip.io": true}
	if len(h) < 1 || len(h) > 253 || reserved[h] || strings.HasSuffix(h, ".sslip.io") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".test") || strings.HasSuffix(h, ".localhost") || net.ParseIP(h) != nil {
		return "", ErrInvalid
	}
	for _, c := range h {
		if c > 127 || !(c == '.' || c == '-' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return "", ErrInvalid
		}
	}
	parts := strings.Split(h, ".")
	if len(parts) < 2 {
		return "", ErrInvalid
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 63 || p[0] == '-' || p[len(p)-1] == '-' {
			return "", ErrInvalid
		}
	}
	return h, nil
}

func (s *Store) CustomDomains(ctx context.Context, token, project string) ([]CustomDomain, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var workspace string
	if err = tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=?", project).Scan(&workspace); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDenied
	} else if err != nil {
		return nil, err
	}
	if _, err = s.authorize(ctx, tx, token, workspace, false); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,project_id,hostname,token,state,message,created_at FROM project_domains WHERE project_id=? ORDER BY created_at,id", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomDomain{}
	for rows.Next() {
		var d CustomDomain
		if err = rows.Scan(&d.ID, &d.Project, &d.Hostname, &d.Token, &d.State, &d.Message, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *Store) CreateCustomDomain(ctx context.Context, token, project, hostname string) (CustomDomain, error) {
	hostname, err := validCustomHostname(hostname)
	if err != nil {
		return CustomDomain{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CustomDomain{}, err
	}
	defer tx.Rollback()
	var workspace string
	if err = tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=?", project).Scan(&workspace); errors.Is(err, sql.ErrNoRows) {
		return CustomDomain{}, ErrDenied
	} else if err != nil {
		return CustomDomain{}, err
	}
	actor, err := s.authorize(ctx, tx, token, workspace, true)
	if err != nil {
		return CustomDomain{}, err
	}
	var deleting int
	if err = tx.QueryRowContext(ctx, "SELECT deletion_requested_at<>0 FROM projects WHERE id=?", project).Scan(&deleting); err != nil {
		return CustomDomain{}, err
	}
	if deleting != 0 {
		return CustomDomain{}, ErrProjectDeleting
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM project_domains WHERE project_id=?", project).Scan(&count); err != nil {
		return CustomDomain{}, err
	}
	if count >= 5 {
		return CustomDomain{}, ErrDomainLimit
	}
	d := CustomDomain{ID: randomToken(), Project: project, Hostname: hostname, Token: randomToken(), State: "pending", CreatedAt: s.now().Unix()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project_domains(id,project_id,hostname,token,state,message,created_at) VALUES(?,?,?,?,?,?,?)", d.ID, d.Project, d.Hostname, d.Token, d.State, "", d.CreatedAt); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return CustomDomain{}, ErrExists
		}
		return CustomDomain{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "project-domain.created", d.CreatedAt); err != nil {
		return CustomDomain{}, err
	}
	return d, tx.Commit()
}

func (s *Store) VerifyCustomDomain(ctx context.Context, token, project, id string, txt, arecs, aaaas []string) (CustomDomain, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CustomDomain{}, err
	}
	defer tx.Rollback()
	var d CustomDomain
	var workspace string
	err = tx.QueryRowContext(ctx, "SELECT d.id,d.project_id,d.hostname,d.token,d.state,d.message,d.created_at,p.workspace_id FROM project_domains d JOIN projects p ON p.id=d.project_id WHERE d.id=? AND d.project_id=?", id, project).Scan(&d.ID, &d.Project, &d.Hostname, &d.Token, &d.State, &d.Message, &d.CreatedAt, &workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return CustomDomain{}, ErrDenied
	}
	if err != nil {
		return CustomDomain{}, err
	}
	if d.State == "removing" {
		return CustomDomain{}, ErrDenied
	}
	actor, err := s.authorize(ctx, tx, token, workspace, true)
	if err != nil {
		return CustomDomain{}, err
	}
	var deleting int
	if err = tx.QueryRowContext(ctx, "SELECT deletion_requested_at<>0 FROM projects WHERE id=?", project).Scan(&deleting); err != nil {
		return CustomDomain{}, err
	}
	if deleting != 0 {
		return CustomDomain{}, ErrProjectDeleting
	}
	wantTXT := "launchstead-verification=" + d.Token
	goodTXT := false
	for _, v := range txt {
		if strings.TrimSpace(v) == wantTXT {
			goodTXT = true
			break
		}
	}
	if len(arecs) != 1 || arecs[0] != customDomainTarget || len(aaaas) != 0 || !goodTXT {
		d.Message = "DNS records do not match required values"
		if _, err = tx.ExecContext(ctx, "UPDATE project_domains SET message=? WHERE id=?", d.Message, d.ID); err != nil {
			return CustomDomain{}, err
		}
		if err = tx.Commit(); err != nil {
			return CustomDomain{}, err
		}
		return d, ErrDomainDNS
	}
	if d.State != "active" {
		d.State = "verified"
		d.Message = "DNS verified; activation is pending"
	} else {
		d.Message = "Active"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE project_domains SET state=?,message=? WHERE id=?", d.State, d.Message, d.ID); err != nil {
		return CustomDomain{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "project-domain.verified", s.now().Unix()); err != nil {
		return CustomDomain{}, err
	}
	return d, tx.Commit()
}

func (s *Store) RemoveCustomDomain(ctx context.Context, token, project, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workspace string
	if err = tx.QueryRowContext(ctx, "SELECT p.workspace_id FROM project_domains d JOIN projects p ON p.id=d.project_id WHERE d.id=? AND d.project_id=?", id, project).Scan(&workspace); errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	} else if err != nil {
		return err
	}
	actor, err := s.authorize(ctx, tx, token, workspace, true)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE project_domains SET state='removing',message=? WHERE id=?", "Removal queued", id); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, workspace, "project-domain.removing", s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

type DNSResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
	LookupA(context.Context, string) ([]string, error)
	LookupAAAA(context.Context, string) ([]string, error)
}
type NetDNSResolver struct {
	Resolver *net.Resolver
}

func (r NetDNSResolver) LookupTXT(ctx context.Context, h string) ([]string, error) {
	return r.Resolver.LookupTXT(ctx, h)
}
func (r NetDNSResolver) LookupA(ctx context.Context, h string) ([]string, error) {
	ips, err := r.Resolver.LookupIP(ctx, "ip4", h)
	out := make([]string, len(ips))
	for i, v := range ips {
		out[i] = v.String()
	}
	return out, err
}
func (r NetDNSResolver) LookupAAAA(ctx context.Context, h string) ([]string, error) {
	ips, err := r.Resolver.LookupIP(ctx, "ip6", h)
	out := make([]string, len(ips))
	for i, v := range ips {
		out[i] = v.String()
	}
	return out, err
}

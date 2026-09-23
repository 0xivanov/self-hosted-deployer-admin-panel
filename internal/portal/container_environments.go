package portal

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrContainerEnvironmentLimit   = errors.New("container environment limit reached")
	ErrContainerEnvironmentInvalid = errors.New("invalid container environment")
)

const maxContainerEnvironments = 50

type ContainerEnvironment struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	Label     string   `json:"label"`
	Names     []string `json:"names"`
	CreatedAt int64    `json:"created_at"`
}

type ContainerEnvironments struct {
	store *Store
	key   [32]byte
}

func NewContainerEnvironments(store *Store, key []byte) (*ContainerEnvironments, error) {
	if store == nil || len(key) != 32 {
		return nil, ErrInvalid
	}
	environments := &ContainerEnvironments{store: store}
	copy(environments.key[:], key)
	return environments, nil
}

func (e *ContainerEnvironments) Create(ctx context.Context, token, project, requestKey, label string, values map[string]string) (ContainerEnvironment, error) {
	var zero ContainerEnvironment
	if values == nil {
		values = map[string]string{}
	}
	if !e.store.containerProjects {
		return zero, ErrContainerUnavailable
	}
	names, err := validContainerEnvironmentInput(requestKey, label, values)
	if err != nil {
		return zero, err
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	p, actor, err := e.store.containerAccess(ctx, tx, token, project)
	if err != nil {
		return zero, err
	}
	var oldID, oldLabel, oldNames string
	var oldCiphertext []byte
	var oldCreated int64
	err = tx.QueryRowContext(ctx, `SELECT id,label,names,ciphertext,created_at FROM container_environments WHERE project_id=? AND request_key=?`, project, requestKey).Scan(&oldID, &oldLabel, &oldNames, &oldCiphertext, &oldCreated)
	if err == nil {
		var storedNames []string
		if json.Unmarshal([]byte(oldNames), &storedNames) != nil {
			return zero, ErrConflict
		}
		old := ContainerEnvironment{ID: oldID, ProjectID: project, Label: oldLabel, Names: storedNames, CreatedAt: oldCreated}
		stored, decryptErr := e.decrypt(project, old.ID, oldCiphertext)
		if decryptErr != nil || old.Label != label || !reflect.DeepEqual(stored, values) {
			return zero, ErrConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM container_environments WHERE project_id=?`, project).Scan(&count); err != nil {
		return zero, err
	}
	if count >= maxContainerEnvironments {
		return zero, ErrContainerEnvironmentLimit
	}
	id := randomToken()
	ciphertext, err := e.encrypt(project, id, values)
	if err != nil {
		return zero, err
	}
	rawNames, err := json.Marshal(names)
	if err != nil {
		return zero, err
	}
	created := e.store.now().Unix()
	if created == 0 {
		created = 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO container_environments(id,project_id,request_key,label,names,ciphertext,created_at) VALUES(?,?,?,?,?,?,?)`, id, project, requestKey, label, rawNames, ciphertext, created); err != nil {
		return zero, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "container.environments.created:"+id, created); err != nil {
		return zero, err
	}
	return ContainerEnvironment{ID: id, ProjectID: project, Label: label, Names: names, CreatedAt: created}, tx.Commit()
}

func (e *ContainerEnvironments) List(ctx context.Context, token, project string) ([]ContainerEnvironment, error) {
	if !e.store.containerProjects {
		return nil, ErrContainerUnavailable
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, _, err := e.store.uploadProject(ctx, tx, token, project, false)
	if err != nil {
		return nil, err
	}
	if p.Kind != "container" {
		return nil, ErrInvalid
	}
	if _, err = e.store.authorize(ctx, tx, token, p.WorkspaceID, true); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id,label,names,created_at FROM container_environments WHERE project_id=? ORDER BY created_at,id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContainerEnvironment{}
	for rows.Next() {
		var item ContainerEnvironment
		var rawNames string
		if err = rows.Scan(&item.ID, &item.ProjectID, &item.Label, &rawNames, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(rawNames), &item.Names); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (e *ContainerEnvironments) Resolve(ctx context.Context, project, id string) (map[string]string, error) {
	var ciphertext []byte
	err := e.store.db.QueryRowContext(ctx, `SELECT ciphertext FROM container_environments WHERE project_id=? AND id=?`, project, id).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	return e.decrypt(project, id, ciphertext)
}

func (e *ContainerEnvironments) encrypt(project, id string, values map[string]string) ([]byte, error) {
	block, err := aes.NewCipher(e.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, payload, environmentAAD(project, id)), nil
}

func (e *ContainerEnvironments) decrypt(project, id string, ciphertext []byte) (map[string]string, error) {
	block, err := aes.NewCipher(e.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(ciphertext) < gcm.NonceSize() {
		return nil, ErrDenied
	}
	payload, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], environmentAAD(project, id))
	if err != nil {
		return nil, ErrDenied
	}
	values := map[string]string{}
	if json.Unmarshal(payload, &values) != nil {
		return nil, ErrDenied
	}
	return values, nil
}

func environmentAAD(project, id string) []byte {
	return []byte("environment-v1\x00" + project + "\x00" + id)
}

func validContainerEnvironmentInput(requestKey, label string, values map[string]string) ([]string, error) {
	if len(values) > 64 || len(requestKey) < 16 || len(requestKey) > 128 || label == "" || len(label) > 80 || !utf8.ValidString(label) {
		return nil, ErrContainerEnvironmentInvalid
	}
	for _, r := range label {
		if r < 32 || r == 127 {
			return nil, ErrContainerEnvironmentInvalid
		}
	}
	names := make([]string, 0, len(values))
	total := 0
	for name, value := range values {
		if !validEnvironmentName(name) || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || len(value) > 8192 {
			return nil, ErrContainerEnvironmentInvalid
		}
		total += len(name) + len(value)
		if total > 32768 {
			return nil, ErrContainerEnvironmentInvalid
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func validEnvironmentName(name string) bool {
	if name == "" || len(name) > 128 || !utf8.ValidString(name) {
		return false
	}
	for i, r := range name {
		if (i == 0 && !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_')) ||
			(i > 0 && !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_')) {
			return false
		}
	}
	return true
}

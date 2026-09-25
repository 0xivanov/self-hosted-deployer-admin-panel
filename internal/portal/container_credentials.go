package portal

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

var (
	ErrContainerCredentialLimit   = errors.New("container credential limit reached")
	ErrContainerCredentialInvalid = errors.New("invalid container credential")
)

const maxContainerCredentials = 20

type ContainerCredential struct {
	Deletable bool   `json:"deletable"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Label     string `json:"label"`
	Registry  string `json:"registry"`
	CreatedAt int64  `json:"created_at"`
}

type ContainerCredentials struct {
	store *Store
	key   [32]byte
}

type containerCredentialPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func NewContainerCredentials(store *Store, key []byte) (*ContainerCredentials, error) {
	if store == nil || len(key) != 32 {
		return nil, ErrInvalid
	}
	credentials := &ContainerCredentials{store: store}
	copy(credentials.key[:], key)
	return credentials, nil
}

func (c *ContainerCredentials) Create(ctx context.Context, token, project, requestKey, label, registry string, credentials registryimage.Credentials) (ContainerCredential, error) {
	var zero ContainerCredential
	if !c.store.containerProjects {
		return zero, ErrContainerUnavailable
	}
	if !validContainerCredentialInput(requestKey, label, registry, credentials) {
		return zero, ErrContainerCredentialInvalid
	}
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	p, actor, err := c.store.containerAccess(ctx, tx, token, project)
	if err != nil {
		return zero, err
	}
	var oldID string
	var oldLabel, oldRegistry string
	var oldCiphertext []byte
	var oldCreated int64
	err = tx.QueryRowContext(ctx, `SELECT id,label,registry,ciphertext,created_at FROM container_credentials WHERE project_id=? AND request_key=?`, project, requestKey).Scan(&oldID, &oldLabel, &oldRegistry, &oldCiphertext, &oldCreated)
	if err == nil {
		old := ContainerCredential{ID: oldID, ProjectID: project, Label: oldLabel, Registry: oldRegistry, CreatedAt: oldCreated}
		payload, decryptErr := c.decrypt(project, old.ID, old.Registry, oldCiphertext)
		if decryptErr != nil || old.Label != label || old.Registry != registry || payload != credentials {
			return zero, ErrConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM container_credentials WHERE project_id=?`, project).Scan(&count); err != nil {
		return zero, err
	}
	if count >= maxContainerCredentials {
		return zero, ErrContainerCredentialLimit
	}
	id := randomToken()
	ciphertext, err := c.encrypt(project, id, registry, credentials)
	if err != nil {
		return zero, err
	}
	created := c.store.now().Unix()
	if created == 0 {
		created = 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO container_credentials(id,project_id,request_key,registry,label,ciphertext,created_at) VALUES(?,?,?,?,?,?,?)`, id, project, requestKey, registry, label, ciphertext, created); err != nil {
		return zero, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "container.credentials.created:"+id, created); err != nil {
		return zero, err
	}
	return ContainerCredential{ID: id, ProjectID: project, Label: label, Registry: registry, CreatedAt: created}, tx.Commit()
}

func (c *ContainerCredentials) List(ctx context.Context, token, project string) ([]ContainerCredential, error) {
	if !c.store.containerProjects {
		return nil, ErrContainerUnavailable
	}
	tx, err := c.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, _, err := c.store.uploadProject(ctx, tx, token, project, false)
	if err != nil {
		return nil, err
	}
	if p.Kind != "container" {
		return nil, ErrInvalid
	}
	if _, err = c.store.authorize(ctx, tx, token, p.WorkspaceID, true); err != nil {
		return nil, err
	}
	references, _, err := containerSettingsReferences(ctx, tx, project)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id,label,registry,created_at FROM container_credentials WHERE project_id=? ORDER BY created_at,id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContainerCredential{}
	for rows.Next() {
		var credential ContainerCredential
		if err = rows.Scan(&credential.ID, &credential.ProjectID, &credential.Label, &credential.Registry, &credential.CreatedAt); err != nil {
			return nil, err
		}
		credential.Deletable = !p.Deleting && !references[credential.ID]
		out = append(out, credential)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (c *ContainerCredentials) Resolve(ctx context.Context, project, id, reference string) (registryimage.Credentials, error) {
	ref, err := registryimage.Parse(reference)
	if err != nil {
		return registryimage.Credentials{}, ErrInvalid
	}
	var registry string
	var ciphertext []byte
	err = c.store.db.QueryRowContext(ctx, `SELECT registry,ciphertext FROM container_credentials WHERE project_id=? AND id=?`, project, id).Scan(&registry, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return registryimage.Credentials{}, ErrDenied
	}
	if err != nil {
		return registryimage.Credentials{}, err
	}
	if registry != ref.Registry {
		return registryimage.Credentials{}, ErrDenied
	}
	return c.decrypt(project, id, registry, ciphertext)
}

func (c *ContainerCredentials) encrypt(project, id, registry string, credentials registryimage.Credentials) ([]byte, error) {
	block, err := aes.NewCipher(c.key[:])
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
	payload, err := json.Marshal(containerCredentialPayload{Username: credentials.Username, Password: credentials.Password})
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, payload, credentialAAD(project, id, registry)), nil
}

func (c *ContainerCredentials) decrypt(project, id, registry string, ciphertext []byte) (registryimage.Credentials, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return registryimage.Credentials{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(ciphertext) < gcm.NonceSize() {
		return registryimage.Credentials{}, ErrDenied
	}
	payload, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], credentialAAD(project, id, registry))
	if err != nil {
		return registryimage.Credentials{}, ErrDenied
	}
	var decoded containerCredentialPayload
	if json.Unmarshal(payload, &decoded) != nil {
		return registryimage.Credentials{}, ErrDenied
	}
	return registryimage.Credentials{Username: decoded.Username, Password: decoded.Password}, nil
}

func credentialAAD(project, id, registry string) []byte {
	return []byte("v1\x00" + project + "\x00" + id + "\x00" + registry)
}

func validContainerCredentialInput(requestKey, label, registry string, credentials registryimage.Credentials) bool {
	if len(requestKey) < 16 || len(requestKey) > 128 || label == "" || len(label) > 80 || !utf8.ValidString(label) || strings.ContainsAny(label, "\r\n") {
		return false
	}
	for _, r := range label {
		if r < 32 || r == 127 {
			return false
		}
	}
	if registry != "docker.io" && registry != "ghcr.io" {
		return false
	}
	return validCredentialPart(credentials.Username, 256, true) && validCredentialPart(credentials.Password, 8192, false)
}

func validCredentialPart(value string, max int, rejectColon bool) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 || (rejectColon && r == ':') {
			return false
		}
	}
	return true
}

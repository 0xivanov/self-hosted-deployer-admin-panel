// Package githubdeploy contains the trust-boundary validation used by the
// GitHub App deployment integration.
package githubdeploy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxPushPayloadSize = 2 << 20

var (
	ErrInvalidSignature = errors.New("invalid GitHub webhook signature")
	ErrInvalidPayload   = errors.New("invalid GitHub push payload")
)

// Push is the validated, authorization-neutral portion of a GitHub push
// delivery. Delivery and event headers are deliberately outside this type.
type Push struct {
	InstallationID     int64
	RepositoryID       int64
	RepositoryFullName string
	Ref                string
	Before             string
	After              string
	Deleted            bool
}

type pushPayload struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// PayloadSHA256 returns the lowercase SHA-256 digest of the exact webhook
// body. Callers can use it as a durable replay identity after authorization.
func PayloadSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// VerifyPush authenticates and validates a GitHub push payload. Signature
// verification is performed over the raw body before JSON parsing. The
// verifier does not authorize a project, prevent replay, or trust delivery and
// event headers; callers must apply those policies separately.
func VerifyPush(secret []byte, signature string, payload []byte) (Push, error) {
	if err := verifySignature(secret, signature, payload); err != nil {
		return Push{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	var raw pushPayload
	if err := decoder.Decode(&raw); err != nil {
		return Push{}, ErrInvalidPayload
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Push{}, ErrInvalidPayload
	}
	if !validPush(raw) {
		return Push{}, ErrInvalidPayload
	}
	return Push{
		InstallationID:     raw.Installation.ID,
		RepositoryID:       raw.Repository.ID,
		RepositoryFullName: raw.Repository.FullName,
		Ref:                raw.Ref,
		Before:             strings.ToLower(raw.Before),
		After:              strings.ToLower(raw.After),
		Deleted:            raw.Deleted,
	}, nil
}

func verifySignature(secret []byte, signature string, payload []byte) error {
	if len(secret) == 0 || len(signature) != len("sha256=")+sha256.Size*2 || !strings.HasPrefix(signature, "sha256=") {
		return ErrInvalidSignature
	}
	want, err := hex.DecodeString(signature[len("sha256="):])
	if err != nil || len(want) != sha256.Size {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	got := mac.Sum(nil)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidSignature
	}
	return nil
}

func validPush(raw pushPayload) bool {
	if raw.Installation.ID <= 0 || raw.Repository.ID <= 0 || !validRepository(raw.Repository.FullName, raw.Repository.Owner.Login) || !validBranchRef(raw.Ref) {
		return false
	}
	if !validSHA(raw.Before) || !validSHA(raw.After) {
		return false
	}
	zero := strings.Repeat("0", 40)
	if strings.EqualFold(raw.After, zero) != raw.Deleted {
		return false
	}
	return true
}

func validRepository(fullName, owner string) bool {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] != owner {
		return false
	}
	return validGitHubName(parts[0], 39) && validGitHubName(parts[1], 100)
}

func validGitHubName(value string, maxBytes int) bool {
	if value == "." || value == ".." || len(value) == 0 || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20
}

func validBranchRef(ref string) bool {
	if !utf8.ValidString(ref) || strings.ContainsFunc(ref, unicode.IsControl) || len(ref) > 1024 || !strings.HasPrefix(ref, "refs/heads/") {
		return false
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if branch == "" || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") {
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return !strings.ContainsAny(branch, " ~^:?*[\\\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f")
}

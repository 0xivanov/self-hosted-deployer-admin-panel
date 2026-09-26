package namesilo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// SandboxAttempt describes an operator sandbox exercise, not a paid customer
// order. Path must identify one immutable attempt in an operator-owned private
// directory. Never use a fresh path to retry an uncertain registrar operation.
type SandboxAttempt struct {
	Operation string `json:"operation"`
	Domain    string `json:"domain"`
	ContactID string `json:"contact_id,omitempty"`
}
type attemptReceipt struct {
	Fingerprint string         `json:"fingerprint"`
	Result      MutationResult `json:"result"`
}

// ExecuteOnce persists intent before any network call. An unfinished attempt
// remains unknown across process restarts. A completed identical request replays
// its receipt without contacting the registrar. This does not reconcile unknown
// outcomes or provide payment authorization; production workers need both.
func (w *SandboxWriter) ExecuteOnce(ctx context.Context, path string, attempt SandboxAttempt) (MutationResult, error) {
	if attempt.Operation != "registerDomain" && attempt.Operation != "renewDomain" {
		return MutationResult{}, ErrInvalid
	}
	raw, _ := json.Marshal(attempt)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return readReceipt(path, fingerprint)
	}
	if err != nil {
		return MutationResult{}, ErrInvalid
	}
	// The first JSON record is a digest only, never credentials or contact data.
	if err = json.NewEncoder(f).Encode(map[string]string{"fingerprint": fingerprint}); err == nil {
		err = f.Sync()
	}
	if err == nil {
		var dir *os.File
		dir, err = os.Open(filepath.Dir(path))
		if err == nil {
			err = dir.Sync()
			dir.Close()
		}
	}
	if err != nil {
		f.Close()
		return MutationResult{}, ErrOutcomeUnknown
	}
	var result MutationResult
	if attempt.Operation == "registerDomain" {
		result, err = w.Register(ctx, attempt.Domain, attempt.ContactID)
	} else {
		result, err = w.Renew(ctx, attempt.Domain)
	}
	if err != nil {
		f.Close()
		return MutationResult{}, err
	}
	if err = json.NewEncoder(f).Encode(attemptReceipt{Fingerprint: fingerprint, Result: result}); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return MutationResult{}, ErrOutcomeUnknown
	}
	return result, nil
}
func readReceipt(path, fingerprint string) (MutationResult, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return MutationResult{}, ErrOutcomeUnknown
	}
	f, err := os.Open(path)
	if err != nil {
		return MutationResult{}, ErrOutcomeUnknown
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 4097))
	dec.DisallowUnknownFields()
	var intent struct {
		Fingerprint string `json:"fingerprint"`
	}
	var receipt attemptReceipt
	if dec.Decode(&intent) != nil || intent.Fingerprint != fingerprint || dec.Decode(&receipt) != nil || receipt.Fingerprint != fingerprint || dec.Decode(new(any)) != io.EOF || receipt.Result.Environment != "sandbox" {
		return MutationResult{}, ErrOutcomeUnknown
	}
	return receipt.Result, nil
}

package githubdeploy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestVerifyPushAcceptsAuthenticatedPayloadAndIgnoresAdditionalFields(t *testing.T) {
	payload := []byte(`{"ref":"refs/heads/main","before":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","after":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","deleted":false,"repository":{"id":42,"full_name":"octocat/Hello-World","owner":{"login":"octocat"},"extra":"allowed"},"installation":{"id":7},"sender":{"login":"octocat"}}`)
	push, err := VerifyPush([]byte("test-secret"), sign("test-secret", payload), payload)
	if err != nil {
		t.Fatalf("VerifyPush: %v", err)
	}
	if push.InstallationID != 7 || push.RepositoryID != 42 || push.RepositoryFullName != "octocat/Hello-World" || push.Ref != "refs/heads/main" || push.Before != strings.Repeat("a", 40) || push.After != strings.Repeat("b", 40) || push.Deleted {
		t.Fatalf("unexpected push: %+v", push)
	}
}

func TestVerifyPushUsesGitHubOfficialSignatureVector(t *testing.T) {
	secret := []byte("It's a Secret to Everybody")
	payload := []byte("Hello, World!")
	signature := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if err := verifySignature(secret, signature, payload); err != nil {
		t.Fatalf("official vector: %v", err)
	}
}

func TestVerifyPushRejectsBadSignatureBeforeJSON(t *testing.T) {
	_, err := VerifyPush([]byte("secret"), "sha256="+strings.Repeat("0", 64), []byte("not-json"))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v, want invalid signature", err)
	}
}

func TestVerifyPushRejectsTrailingJSONAndInvalidMetadata(t *testing.T) {
	base := `{"ref":"refs/heads/main","before":"1111111111111111111111111111111111111111","after":"2222222222222222222222222222222222222222","deleted":false,"repository":{"id":42,"full_name":"octocat/Hello-World","owner":{"login":"octocat"}},"installation":{"id":7}}`
	for name, payload := range map[string]string{
		"trailing":        base + ` {}`,
		"repo dot":        strings.Replace(base, "octocat/Hello-World", "octocat/..", 1),
		"repo query":      strings.Replace(base, "octocat/Hello-World", "octocat/repo?query", 1),
		"wrong owner":     strings.Replace(base, `"login":"octocat"`, `"login":"other"`, 1),
		"wrong ref":       strings.Replace(base, `refs/heads/main`, `refs/tags/v1`, 1),
		"zero after live": strings.Replace(base, `2222222222222222222222222222222222222222`, strings.Repeat("0", 40), 1),
		"double slash":    strings.Replace(base, `refs/heads/main`, `refs/heads/a//b`, 1),
		"dot component":   strings.Replace(base, `refs/heads/main`, `refs/heads/.hidden`, 1),
		"lock component":  strings.Replace(base, `refs/heads/main`, `refs/heads/main.lock`, 1),
		"trailing dot":    strings.Replace(base, `refs/heads/main`, `refs/heads/main.`, 1),
		"at brace":        strings.Replace(base, `refs/heads/main`, `refs/heads/a@{b`, 1),
		"too long":        strings.Replace(base, `refs/heads/main`, `refs/heads/`+strings.Repeat("a", 1014), 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := VerifyPush([]byte("secret"), sign("secret", []byte(payload)), []byte(payload))
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("error = %v, want invalid payload", err)
			}
		})
	}
}

func TestVerifyPushAcceptsDeletionZeroAfter(t *testing.T) {
	payload := []byte(`{"ref":"refs/heads/feature/remove","before":"1111111111111111111111111111111111111111","after":"0000000000000000000000000000000000000000","deleted":true,"repository":{"id":42,"full_name":"octocat/Hello-World","owner":{"login":"octocat"}},"installation":{"id":7}}`)
	if _, err := VerifyPush([]byte("secret"), sign("secret", payload), payload); err != nil {
		t.Fatalf("VerifyPush deletion: %v", err)
	}
}

func TestVerifyPushRejectsSignatureAndPayloadLimits(t *testing.T) {
	if _, err := VerifyPush(nil, "", nil); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("empty inputs error = %v", err)
	}
	payload := []byte(strings.Repeat("x", maxPushPayloadSize+1))
	if _, err := VerifyPush([]byte("secret"), sign("secret", payload), payload); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestPayloadSHA256(t *testing.T) {
	want := sha256.Sum256([]byte("payload"))
	if got := PayloadSHA256([]byte("payload")); got != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %s, want %s", got, hex.EncodeToString(want[:]))
	}
}

func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

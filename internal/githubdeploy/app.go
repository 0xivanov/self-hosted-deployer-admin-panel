package githubdeploy

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxAppClientIDBytes = 256
	maxAppKeyPEMBytes   = 16 << 10
	appJWTLifetime      = 9 * time.Minute
	appJWTClockSkew     = time.Minute
)

var ErrAppConfig = errors.New("invalid GitHub App configuration")

// App contains the private signing material for one GitHub App. The key is
// deliberately unexported so it cannot be serialized or logged accidentally.
type App struct {
	clientID string
	key      *rsa.PrivateKey
}

// NewApp validates and stores a GitHub App client ID and RSA private key.
// keyPEM must contain exactly one PKCS#1 or PKCS#8 RSA private key block.
func NewApp(clientID string, keyPEM []byte) (*App, error) {
	if !validAppClientID(clientID) || len(keyPEM) == 0 || len(keyPEM) > maxAppKeyPEMBytes {
		return nil, ErrAppConfig
	}
	trimmed := strings.TrimSpace(string(keyPEM))
	if !strings.HasPrefix(trimmed, "-----BEGIN RSA PRIVATE KEY-----") && !strings.HasPrefix(trimmed, "-----BEGIN PRIVATE KEY-----") {
		return nil, ErrAppConfig
	}
	block, rest := pem.Decode([]byte(trimmed))
	if block == nil || len(block.Headers) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, ErrAppConfig
	}
	var key *rsa.PrivateKey
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				return nil, ErrAppConfig
			}
		}
	default:
		return nil, ErrAppConfig
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, ErrAppConfig
	}
	if err := key.Validate(); err != nil {
		return nil, ErrAppConfig
	}
	return &App{clientID: clientID, key: key}, nil
}

func validAppClientID(value string) bool {
	if value == "" || len(value) > maxAppClientIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// String redacts the client ID and private key from generic formatting.
func (a App) String() string { return "[github app credentials redacted]" }

// GoString redacts the client ID and private key from %#v formatting.
func (a App) GoString() string { return a.String() }

func (a *App) appJWT(now time.Time) (string, error) {
	if a == nil || a.key == nil || !validAppClientID(a.clientID) || now.IsZero() {
		return "", ErrAppConfig
	}
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{Algorithm: "RS256", Type: "JWT"})
	if err != nil {
		return "", ErrAppConfig
	}
	claims, err := json.Marshal(struct {
		IssuedAt int64  `json:"iat"`
		Expires  int64  `json:"exp"`
		Issuer   string `json:"iss"`
	}{IssuedAt: now.Unix() - int64(appJWTClockSkew/time.Second), Expires: now.Unix() + int64(appJWTLifetime/time.Second), Issuer: a.clientID})
	if err != nil {
		return "", ErrAppConfig
	}
	encode := base64.RawURLEncoding.EncodeToString
	message := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", ErrAppConfig
	}
	return message + "." + encode(signature), nil
}

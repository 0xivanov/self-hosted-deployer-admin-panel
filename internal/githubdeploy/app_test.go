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
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNewAppJWTClaimsAndSignature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app, err := NewApp("123456", keyPEM)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	token, err := app.appJWT(now)
	if err != nil {
		t.Fatalf("appJWT: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT parts = %d, want 3", len(parts))
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	decode := func(value string, output any) {
		t.Helper()
		data, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, output); err != nil {
			t.Fatal(err)
		}
	}
	decode(parts[0], &header)
	if header.Algorithm != "RS256" || header.Type != "JWT" {
		t.Fatalf("header = %+v", header)
	}
	var claims struct {
		IssuedAt int64  `json:"iat"`
		Expires  int64  `json:"exp"`
		Issuer   string `json:"iss"`
	}
	decode(parts[1], &claims)
	if claims.Issuer != "123456" || claims.IssuedAt != now.Unix()-60 || claims.Expires != now.Unix()+9*60 {
		t.Fatalf("claims = %+v", claims)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("signature: %v", err)
	}
}

func TestNewAppAcceptsPKCS8RSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewApp("app-client", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		t.Fatalf("NewApp PKCS8: %v", err)
	}
}

func TestNewAppRejectsInvalidKeyInputs(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	validKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	validPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(validKey)})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: mustMarshalPKIXPublicKey(t, &validKey.PublicKey)})
	weakPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	for name, tc := range map[string][]byte{
		"empty":            nil,
		"leading text":     append([]byte("ignored preamble\n"), validPEM...),
		"PEM headers":      pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Headers: map[string]string{"Comment": "unexpected"}, Bytes: x509.MarshalPKCS1PrivateKey(validKey)}),
		"wrong block":      publicPEM,
		"undersized RSA":   weakPEM,
		"trailing text":    append(append([]byte(nil), validPEM...), []byte("trailing")...),
		"second PEM block": append(append([]byte(nil), validPEM...), validPEM...),
		"invalid DER":      pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("bad")}),
		"oversized":        append(append([]byte(nil), validPEM...), []byte(strings.Repeat("x", maxAppKeyPEMBytes))...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewApp("123", tc); !errors.Is(err, ErrAppConfig) {
				t.Fatalf("error = %v, want ErrAppConfig", err)
			}
		})
	}
	for _, clientID := range []string{"", "has space", "has\nnewline", strings.Repeat("a", maxAppClientIDBytes+1)} {
		t.Run(fmt.Sprintf("client-%d", len(clientID)), func(t *testing.T) {
			if _, err := NewApp(clientID, validPEM); !errors.Is(err, ErrAppConfig) {
				t.Fatalf("error = %v, want ErrAppConfig", err)
			}
		})
	}
}

func TestAppFormattingRedactsCredentials(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp("secret-client", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%v %#v", *app, *app)
	if strings.Contains(formatted, "secret-client") || strings.Contains(formatted, "PRIVATE KEY") {
		t.Fatalf("format leaked app credentials: %s", formatted)
	}
}

func mustMarshalPKIXPublicKey(t *testing.T, key *rsa.PublicKey) []byte {
	t.Helper()
	data, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

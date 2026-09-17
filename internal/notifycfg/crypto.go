package notifycfg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	envelopeVersion = 1
	notifyKeyInfo   = "spore/notification-credentials/v1"
)

type envelope struct {
	Version        int    `json:"version"`
	KeyFingerprint string `json:"key_fingerprint"`
	Nonce          string `json:"nonce"`
	Ciphertext     string `json:"ciphertext"`
}

type cryptor struct {
	key         []byte
	fingerprint string
}

func newCryptor(rootKey []byte) cryptor {
	if len(rootKey) != 32 {
		return cryptor{}
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, rootKey, nil, []byte(notifyKeyInfo)), key); err != nil {
		return cryptor{}
	}
	sum := sha256.Sum256(key)
	return cryptor{key: key, fingerprint: base64.RawURLEncoding.EncodeToString(sum[:8])}
}

func (c cryptor) encrypt(plain, field string) (*envelope, error) {
	if plain == "" {
		return nil, nil
	}
	if len(c.key) != 32 {
		return nil, ErrKeyUnavailable
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("生成通知凭据随机数失败")
	}
	ciphertext := aead.Seal(nil, nonce, []byte(plain), []byte(field))
	return &envelope{
		Version:        envelopeVersion,
		KeyFingerprint: c.fingerprint,
		Nonce:          base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext:     base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func (c cryptor) decrypt(e *envelope, field string) (string, error) {
	if e == nil {
		return "", nil
	}
	if len(c.key) != 32 {
		return "", ErrKeyUnavailable
	}
	if e.Version != envelopeVersion || e.KeyFingerprint == "" || e.Nonce == "" || e.Ciphertext == "" {
		return "", ErrCredentialCorrupt
	}
	if subtle.ConstantTimeCompare([]byte(e.KeyFingerprint), []byte(c.fingerprint)) != 1 {
		return "", ErrKeyMismatch
	}
	nonce, err := base64.RawURLEncoding.DecodeString(e.Nonce)
	if err != nil {
		return "", ErrCredentialCorrupt
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(e.Ciphertext)
	if err != nil {
		return "", ErrCredentialCorrupt
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", ErrCredentialCorrupt
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != aead.NonceSize() {
		return "", ErrCredentialCorrupt
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte(field))
	if err != nil {
		return "", ErrCredentialCorrupt
	}
	return string(plain), nil
}

func credentialState(err error) CredentialState {
	if err == nil {
		return CredentialState{Available: true}
	}
	switch {
	case errors.Is(err, ErrKeyUnavailable):
		return CredentialState{Message: "通知凭据密钥不可用"}
	case errors.Is(err, ErrKeyMismatch):
		return CredentialState{Message: "通知凭据由其他密钥加密，请重新填写"}
	default:
		return CredentialState{Message: "通知凭据不可用，请重新填写"}
	}
}

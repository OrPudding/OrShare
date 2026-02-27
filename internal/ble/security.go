package ble

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"fmt"
)

var (
	localPriv   *ecdsa.PrivateKey
	localPubB64 string
)

func init() {
	var err error
	localPriv, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&localPriv.PublicKey)
	if err != nil {
		panic(err)
	}
	localPubB64 = base64.StdEncoding.EncodeToString(der)
}

func EncodedPublicKeyB64() string {
	return localPubB64
}

// Kotlin:
// agreement.generateSecret("TlsPremasterSecret").encoded
// For ECDH P-256, this is effectively the shared secret (X coordinate) padded to 32 bytes.
func deriveSessionKeyBytes(peerPubB64 string) ([]byte, error) {
	der, err := base64.StdEncoding.DecodeString(peerPubB64)
	if err != nil {
		return nil, err
	}
	pubAny, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	peerPub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("peer key is not ECDSA public key (got %T)", pubAny)
	}
	if peerPub.Curve != elliptic.P256() {
		return nil, fmt.Errorf("peer curve is not P-256")
	}

	// shared = peerPub * localPriv.D
	x, _ := peerPub.Curve.ScalarMult(peerPub.X, peerPub.Y, localPriv.D.Bytes())
	if x == nil {
		return nil, fmt.Errorf("ecdh scalar mult failed")
	}

	// pad to 32 bytes (big-endian)
	xb := x.Bytes()
	if len(xb) > 32 {
		return nil, fmt.Errorf("unexpected shared secret length: %d", len(xb))
	}
	secret := make([]byte, 32)
	copy(secret[32-len(xb):], xb)
	return secret, nil
}

type SessionCipher struct {
	key []byte // 16/24/32 bytes; here expected 32
}

func DeriveSessionCipher(peerPubB64 string) (*SessionCipher, error) {
	k, err := deriveSessionKeyBytes(peerPubB64)
	if err != nil {
		return nil, err
	}
	// Kotlin 直接 SecretKeySpec(secret.encoded,"AES")，不截断
	return &SessionCipher{key: k}, nil
}

var fixedIV = []byte("0102030405060708") // 16 bytes ASCII

func (s *SessionCipher) DecryptB64(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.key) // 32 bytes => AES-256
	if err != nil {
		return "", err
	}
	stream := cipher.NewCTR(block, fixedIV)
	out := make([]byte, len(data))
	stream.XORKeyStream(out, data)
	return string(out), nil
}

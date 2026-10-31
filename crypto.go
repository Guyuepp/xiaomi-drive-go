package xiaomidrive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
)

// loginPublicKey is the account site's public RSA key (1024-bit, X.509
// SubjectPublicKeyInfo, base64). It is a public front-end constant.
const loginPublicKey = "MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCYEVrK/4Mahiv0pUJgTybx4J9P5dUT/Y0PuwMbk+gMU+jrZnBiXGv6/hCH1avIhoBcE535F8nJQQN3UavZdFkYidsoXuEnat3+eVTp3FslyhRwIBDF09v4vDhRtxFOT+R7uH7h/mzmyA2/+lfIMWGIrffXprYizbV76+YQKhoqFQIDAQAB"

const loginAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*"

// loginIV is the fixed CBC IV the web client uses ("0102030405060708").
var loginIV = []byte("0102030405060708")

// randKey returns 16 random characters from loginAlphabet. As all characters
// are ASCII the result is 16 bytes, i.e. an AES-128 key.
func randKey() (string, error) {
	const n = 16
	b := make([]byte, n)
	max := big.NewInt(int64(len(loginAlphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = loginAlphabet[idx.Int64()]
	}
	return string(b), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func aesCBCEncrypt(key, iv, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func rsaEncryptPKCS1v15(pubB64 string, plain []byte) ([]byte, error) {
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return nil, err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("xiaomidrive: login key is not RSA")
	}
	return rsa.EncryptPKCS1v15(rand.Reader, rsaPub, plain)
}

// encryptLoginField encrypts a single account-login field (only "user" is used
// in this library, for the SMS "send code" step). It reproduces the web
// client's scheme:
//
//	K    = 16 random alphabet chars
//	enc  = base64( AES-128-CBC( value, key=K, iv=loginIV, PKCS7 ) )
//	eui  = base64( RSA-PKCS1v1.5( pub, base64(K) ) ) + "." + base64(fieldName)
//
// It returns the eui header value and the encrypted field value.
func encryptLoginField(fieldName, value string) (eui, encrypted string, err error) {
	k, err := randKey()
	if err != nil {
		return "", "", err
	}
	ct, err := aesCBCEncrypt([]byte(k), loginIV, []byte(value))
	if err != nil {
		return "", "", fmt.Errorf("xiaomidrive: aes: %w", err)
	}
	encrypted = base64.StdEncoding.EncodeToString(ct)

	rsaOut, err := rsaEncryptPKCS1v15(loginPublicKey, []byte(base64.StdEncoding.EncodeToString([]byte(k))))
	if err != nil {
		return "", "", fmt.Errorf("xiaomidrive: rsa: %w", err)
	}
	eui = base64.StdEncoding.EncodeToString(rsaOut) + "." +
		base64.StdEncoding.EncodeToString([]byte(fieldName))
	return eui, encrypted, nil
}

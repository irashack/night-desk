package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

func decodeKey(s string) ([]byte, error) {
	enc := base64.RawURLEncoding
	if strings.HasSuffix(s, "=") {
		enc = base64.URLEncoding
	}
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("invalid subscription key")
	}
	return enc.Strict().DecodeString(s)
}

func receiverKeys(p256dh, secret string) (*ecdh.PublicKey, []byte, error) {
	point, err := decodeKey(p256dh)
	if err != nil {
		return nil, nil, err
	}
	receiver, err := ecdh.P256().NewPublicKey(point)
	if err != nil {
		return nil, nil, err
	}
	auth, err := decodeKey(secret)
	if err != nil || len(auth) != 16 {
		return nil, nil, errors.New("invalid authentication secret")
	}
	return receiver, auth, nil
}

// ValidKeys checks a browser subscription’s P-256 point and authentication secret.
func ValidKeys(p256dh, secret string) bool {
	_, _, err := receiverKeys(p256dh, secret)
	return err == nil
}

func encrypt(p256dh, secret string, message []byte) ([]byte, error) {
	receiver, auth, err := receiverKeys(p256dh, secret)
	if err != nil {
		return nil, err
	}
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	var salt [16]byte
	rand.Read(salt[:])
	return sealRecord(sender, receiver, auth, salt[:], message)
}

// sealRecord binds the RFC 8291 key schedule to both public keys, receiver first.
func sealRecord(sender *ecdh.PrivateKey, receiver *ecdh.PublicKey, auth, salt, message []byte) ([]byte, error) {
	const recordSize = 4096
	if len(message)+17 > recordSize {
		return nil, errors.New("push exceeds single record")
	}
	shared, err := sender.ECDH(receiver)
	if err != nil {
		return nil, err
	}
	pub := sender.PublicKey().Bytes()
	info := "WebPush: info\x00" + string(receiver.Bytes()) + string(pub)
	ikm, err := hkdf.Key(sha256.New, shared, auth, info, 32)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := binary.BigEndian.AppendUint32(salt, recordSize)
	header = append(header, byte(len(pub)))
	header = append(header, pub...)
	return gcm.Seal(header, nonce, append(message, 2), nil), nil
}

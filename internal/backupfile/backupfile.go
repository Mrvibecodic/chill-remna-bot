// Package backupfile запирает файл бэкапа паролем.
//
// Устройство: метка формата, соль, nonce и шифротекст AES-256-GCM. Ключ
// выводится из пароля через scrypt, поэтому файл открывается на любом сервере,
// где знают пароль, — ключ шифрования настроек бота для этого не нужен.
// Метка формата входит в аутентифицированные данные: подменить её незаметно
// нельзя.
package backupfile

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"

	"golang.org/x/crypto/scrypt"
)

const (
	magic   = "CRBAK1"
	saltLen = 16
	// Параметры scrypt: около 32 МБ памяти и десятой доли секунды на попытку.
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
)

var (
	// ErrNotBackup — файл не бэкап бота.
	ErrNotBackup = errors.New("это не файл бэкапа бота")
	// ErrPassword — пароль не подходит или файл повреждён.
	ErrPassword = errors.New("пароль не подходит или файл повреждён")
)

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, 32)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal шифрует data паролем.
func Seal(data []byte, password string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(magic)+saltLen+len(nonce)+len(data)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, salt...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, data, []byte(magic)), nil
}

// IsBackup — файл начинается с метки бэкапа.
func IsBackup(file []byte) bool {
	return bytes.HasPrefix(file, []byte(magic))
}

// Open расшифровывает файл паролем.
func Open(file []byte, password string) ([]byte, error) {
	if !IsBackup(file) {
		return nil, ErrNotBackup
	}
	rest := file[len(magic):]
	if len(rest) < saltLen {
		return nil, ErrPassword
	}
	key, err := deriveKey(password, rest[:saltLen])
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	rest = rest[saltLen:]
	if len(rest) < gcm.NonceSize()+gcm.Overhead() {
		return nil, ErrPassword
	}
	plain, err := gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], []byte(magic))
	if err != nil {
		return nil, ErrPassword
	}
	return plain, nil
}

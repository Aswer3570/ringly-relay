// Package bridgelink генерирует ключи Reality и кодирует/декодирует ссылку-мостик.
//
// Ссылка — компактное описание одной точки входа: хост, порт, публичный ключ Reality, short_id, SNI
// и флаги возможностей. В приватном режиме она раздаётся из рук в руки; при автопубликации те же поля
// уходят на control-сервер. Приложение Ringly понимает схему ringly-bridge://.
package bridgelink

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Scheme — префикс ссылки-мостика.
const Scheme = "ringly-bridge://"

// Flow — VLESS flow для Reality (фиксированный).
const Flow = "xtls-rprx-vision"

// Keys — сгенерированные идентификаторы VLESS+Reality.
type Keys struct {
	PrivateKey string // X25519 приватный, base64url raw
	PublicKey  string // X25519 публичный, base64url raw
	ShortID    string // hex, 8 байт
	UUID       string // идентификатор VLESS-пользователя (UUIDv4)
}

// GenerateKeys создаёт пару X25519 для Reality, случайный short_id и UUID VLESS-пользователя.
func GenerateKeys() (Keys, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, fmt.Errorf("генерация ключа Reality: %w", err)
	}
	sid := make([]byte, 8)
	if _, err := rand.Read(sid); err != nil {
		return Keys{}, fmt.Errorf("генерация short_id: %w", err)
	}
	uuid, err := generateUUID()
	if err != nil {
		return Keys{}, err
	}
	enc := base64.RawURLEncoding
	return Keys{
		PrivateKey: enc.EncodeToString(priv.Bytes()),
		PublicKey:  enc.EncodeToString(priv.PublicKey().Bytes()),
		ShortID:    hex.EncodeToString(sid),
		UUID:       uuid,
	}, nil
}

// GenerateSecret возвращает случайный секрет (32 байта, hex) для REST-аутентификации TURN.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("генерация секрета: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// generateUUID возвращает случайный UUIDv4 в каноническом виде 8-4-4-4-12.
func generateUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("генерация UUID: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // версия 4
	b[8] = (b[8] & 0x3f) | 0x80 // вариант RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// PublicFromPrivate выводит публичный ключ Reality из приватного (для `link` и регистрации:
// приватный ключ в конфиге, публичный нигде не хранится).
func PublicFromPrivate(privB64 string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(privB64)
	if err != nil {
		return "", fmt.Errorf("декодирование приватного ключа: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("некорректный приватный ключ Reality: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// Descriptor — то, что кодируется в ссылку и уходит при регистрации.
type Descriptor struct {
	Host      string `json:"h"`             // публичный хост/IP демона
	Port      int    `json:"p"`             // порт мостика (bridge.listen)
	PublicKey string `json:"pk"`            // Reality public key (base64url)
	ShortID   string `json:"sid"`           // hex
	SNI       string `json:"sni"`           // заимствованный домен
	UUID      string `json:"id"`            // VLESS-пользователь
	Msg       bool   `json:"msg"`           // умеет мостик сообщений
	Calls     bool   `json:"call"`          // умеет ретранслятор звонков
	Geo       string `json:"geo,omitempty"` // ISO-код страны, для геоподбора (необязательно)
}

// Encode кодирует дескриптор в ссылку ringly-bridge://<base64url(json)>.
func Encode(d Descriptor) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("сериализация дескриптора: %w", err)
	}
	return Scheme + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode разбирает ссылку-мостик обратно в дескриптор.
func Decode(link string) (Descriptor, error) {
	s := strings.TrimSpace(link)
	if !strings.HasPrefix(s, Scheme) {
		return Descriptor{}, fmt.Errorf("не ссылка-мостик (ожидается префикс %s)", Scheme)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, Scheme))
	if err != nil {
		return Descriptor{}, fmt.Errorf("декодирование ссылки: %w", err)
	}
	var d Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return Descriptor{}, fmt.Errorf("разбор дескриптора: %w", err)
	}
	return d, nil
}

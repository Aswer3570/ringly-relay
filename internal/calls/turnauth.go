package calls

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pion/turn/v4"
)

// restAuthHandler возвращает AuthHandler для coturn-совместимой REST-схемы временных кредов.
//
// Клиент получает от control-сервера (или из ссылки): username = "<expiry-unix>[:uid]",
// password = base64(HMAC-SHA1(secret, username)). Сервер по username сам вычисляет пароль тем же
// секретом и строит ключ длительной аутентификации — совпадёт, только если клиент знал тот же секрет.
// Просроченный username отвергается.
func restAuthHandler(secret, realm string, now func() time.Time) turn.AuthHandler {
	return func(username, _ string, _ net.Addr) ([]byte, bool) {
		if !usernameValid(username, now()) {
			return nil, false
		}
		mac := hmac.New(sha1.New, []byte(secret))
		mac.Write([]byte(username))
		password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		return turn.GenerateAuthKey(username, realm, password), true
	}
}

// usernameValid проверяет срок годности временного username (первое поле до ':' — unix-время
// истечения). Пустой/некорректный срок считаем невалидным.
func usernameValid(username string, now time.Time) bool {
	if username == "" {
		return false
	}
	head := username
	if i := strings.IndexByte(username, ':'); i >= 0 {
		head = username[:i]
	}
	expiry, err := strconv.ParseInt(head, 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() <= expiry
}

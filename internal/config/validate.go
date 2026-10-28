package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Проверки ФОРМАТА полей. «Непустое» ничего не гарантирует: на живой установке в конфиг попадали
// SNI с экранированием из Markdown (`www\.ozon\.ru`) и адрес вместе с разметкой
// (`[https://…](https://…)`), и sing-box падал уже при запуске, с невнятной ошибкой. Ловим это
// здесь, с объяснением, что не так.

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	shortIDRe = regexp.MustCompile(`^([0-9a-f]{2}){1,8}$`) // Reality: hex, чётная длина, до 16 символов
	labelRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// NormalizeHost чинит типичные следы копирования: экранирование Markdown (`www\.site\.ru`),
// схему (`https://site.ru/`), путь и пробелы. Возвращает то, что стоит проверить ValidateHost.
func NormalizeHost(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, `\`, "")
	s = strings.Trim(s, "`'\"<>")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSuffix(s, "."))
}

// NormalizeURL достаёт адрес из Markdown-ссылки `[текст](https://…)` и убирает кавычки и хвостовой «/».
func NormalizeURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`'\"<>")
	if i := strings.Index(s, "]("); i >= 0 && strings.HasSuffix(s, ")") {
		s = s[i+2 : len(s)-1]
	}
	s = strings.Trim(s, "[]")
	return strings.TrimSuffix(s, "/")
}

// ValidateHost — обычное доменное имя (для SNI Reality): без схемы, порта, пути и экранирования.
func ValidateHost(s string) error {
	if s == "" {
		return fmt.Errorf("пусто")
	}
	if strings.ContainsAny(s, `\/:[]() `) {
		return fmt.Errorf("«%s» — нужно одно доменное имя, например www.microsoft.com (без https://, слешей и обратных слешей)", s)
	}
	if net.ParseIP(s) != nil {
		return fmt.Errorf("«%s» — нужен домен, а не IP-адрес: Reality маскируется под настоящий сайт", s)
	}
	labels := strings.Split(strings.ToLower(s), ".")
	if len(labels) < 2 || len(s) > 253 {
		return fmt.Errorf("«%s» — не похоже на доменное имя", s)
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return fmt.Errorf("«%s» — недопустимые символы в доменном имени", s)
		}
	}
	return nil
}

// ValidateURL — адрес сервера: http(s), с хостом, без разметки.
func ValidateURL(s string) error {
	if strings.ContainsAny(s, "[]() ") {
		return fmt.Errorf("«%s» — похоже, адрес скопирован вместе с разметкой; нужен просто https://…", s)
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("«%s» — нужен полный адрес вида https://api.ringlyapp.net", s)
	}
	return nil
}

func validatePrivateKey(s string) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("reality.private_key повреждён (ожидается X25519-ключ, 32 байта в base64url) — удалите строку и запустите `ringly-relay setup`, он сгенерирует новый")
	}
	return nil
}

func validateListen(field, s string) error {
	port, err := PortFromListen(s)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: порт %d вне диапазона 1–65535", field, port)
	}
	return nil
}

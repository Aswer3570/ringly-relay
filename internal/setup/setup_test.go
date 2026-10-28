package setup

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// Сценарий с живой установки: домен вставлен с экранированием из Markdown, порт — просто числом.
// Мастер должен сам всё почистить и выдать рабочий конфиг, не заставляя править его руками.
func TestRunCleansPastedInput(t *testing.T) {
	checkRealityTarget = func(string) error { return nil }
	detectPublicIP = func() (string, error) { return "203.0.113.7", nil }

	answers := strings.Join([]string{
		"",              // мостик: да (по умолчанию)
		"443",           // порт
		`www\.ozon\.ru`, // домен с экранированием
		"",              // звонки: нет
		"private",       // режим
		"",              // публичный адрес: определённый автоматически
	}, "\n") + "\n"

	c, err := Run(strings.NewReader(answers), io.Discard, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.Bridge.Listen != ":443" {
		t.Errorf("listen = %q, want :443", c.Bridge.Listen)
	}
	if c.Reality.SNI != "www.ozon.ru" {
		t.Errorf("sni = %q, want www.ozon.ru", c.Reality.SNI)
	}
	if c.Publish.PublicHost != "203.0.113.7" {
		t.Errorf("public_host = %q, want 203.0.113.7", c.Publish.PublicHost)
	}
	if c.Reality.PrivateKey == "" || c.Reality.UUID == "" {
		t.Error("ключи Reality не сгенерированы")
	}
}

// Недоступный с сервера домен — предупреждение и повторный вопрос, а не молчаливо сломанный мостик.
func TestRunRejectsUnreachableDomain(t *testing.T) {
	checkRealityTarget = func(host string) error {
		if host == "bad.example.com" {
			return errors.New("недоступен")
		}
		return nil
	}
	detectPublicIP = func() (string, error) { return "203.0.113.7", nil }

	answers := strings.Join([]string{
		"", "", // мостик да, порт по умолчанию
		"bad.example.com", "n", // недоступный домен → не оставлять
		"www.microsoft.com", // второй ответ
		"", "private", "",
	}, "\n") + "\n"

	c, err := Run(strings.NewReader(answers), io.Discard, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.Reality.SNI != "www.microsoft.com" {
		t.Errorf("sni = %q, ожидался второй ответ", c.Reality.SNI)
	}
}

// Режим auto не спрашивает секрет регистрации: он вшит в демон, волонтёру его неоткуда знать.
func TestRunAutoModeNeedsNoSecret(t *testing.T) {
	checkRealityTarget = func(string) error { return nil }
	detectPublicIP = func() (string, error) { return "203.0.113.7", nil }

	answers := strings.Join([]string{
		"", "", "", // мостик да, порт и домен по умолчанию
		"",     // звонки: нет
		"auto", // режим
		"",     // публичный адрес: определённый автоматически
	}, "\n") + "\n"

	c, err := Run(strings.NewReader(answers), io.Discard, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.Publish.EnrollSecret != config.DefaultEnrollSecret {
		t.Errorf("enroll_secret = %q, ожидался вшитый", c.Publish.EnrollSecret)
	}
	// адрес обязан попасть в конфиг: за Nginx сервер сам его не узнает
	if c.Publish.PublicHost != "203.0.113.7" {
		t.Errorf("public_host = %q, want 203.0.113.7", c.Publish.PublicHost)
	}
}

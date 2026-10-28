// Package config описывает конфигурацию демона-ретранслятора и его чтение/запись.
//
// Никаких секретов приложения здесь НЕ хранится — только параметры самого мостика:
// адрес origin, куда проксировать, ключи Reality (обфускация), настройки TURN и режим публикации.
// Файл генерируется мастером `setup`, дальше его можно править руками.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultPath — где демон ищет конфиг, если путь не задан флагом.
const DefaultPath = "/etc/ringly-relay/config.toml"

// Config — корневая конфигурация демона.
type Config struct {
	Origin   Origin   `toml:"origin"`
	Bridge   Bridge   `toml:"bridge"`
	Reality  Reality  `toml:"reality"`
	Calls    Calls    `toml:"calls"`
	Publish  Publish  `toml:"publish"`
	Operator Operator `toml:"operator"`

	// StatePath — файл состояния демона (токен регистрации в пуле). Не настройка, а то, что демон
	// пишет сам, поэтому в конфиг не сериализуется; путь задаёт main (рядом с конфигом или
	// RINGLY_RELAY_STATE_DIR).
	StatePath string `toml:"-"`
}

// Origin — куда мостик направляет расшифрованный по обфускации трафик (сам origin остаётся E2EE
// для содержимого; демон видит только шифртекст приложения). Control — куда демон шлёт регистрацию.
type Origin struct {
	URL     string `toml:"url"`     // напр. https://api.ringlyapp.net
	Control string `toml:"control"` // куда автопубликоваться; часто совпадает с URL
}

// Bridge — мостик сообщений: обфусцированный вход VLESS+Reality (терминирует sing-box),
// трафик идёт на запрошенный клиентом origin. SingboxPath — путь к бинарнику sing-box, который
// демон супервизирует (не встраиваем библиотекой: эталонная реализация надёжнее и аудируемее).
type Bridge struct {
	Enabled     bool   `toml:"enabled"`
	Listen      string `toml:"listen"`       // напр. ":443" — маскировка под HTTPS
	SingboxPath string `toml:"singbox_path"` // путь к sing-box; по умолчанию берётся из PATH
}

// Reality — параметры обфускации VLESS+Reality. Приватный ключ и UUID генерируются в `setup`.
// PublicKey выводится из приватного и в конфиге не хранится (его печатает `link` / шлёт регистрация).
type Reality struct {
	SNI        string `toml:"sni"`         // заимствованный домен (реальный, достижимый)
	PrivateKey string `toml:"private_key"` // X25519, base64url (raw)
	ShortID    string `toml:"short_id"`    // hex
	UUID       string `toml:"uuid"`        // идентификатор VLESS-пользователя
}

// Calls — ретранслятор звонков (TURN). Выключен по умолчанию: качает через себя весь голос/видео.
//
// Secret — общий секрет REST-аутентификации (coturn-совместимая схема: клиент получает временные
// creds username="<expiry>[:uid]", password=base64(HMAC-SHA1(secret, username))). PublicIP ОБЯЗАТЕЛЕН
// за NAT: без него relay раздаётся на внутренний адрес и соединение висит (инцидент calls-stability).
type Calls struct {
	Enabled       bool   `toml:"enabled"`
	TurnListen    string `toml:"turn_listen"`     // UDP+TCP, напр. ":3478"
	TurnTLSListen string `toml:"turn_tls_listen"` // TURN-over-TLS, напр. ":5349" (фолбэк под DPI на UDP)
	TrafficCapGB  int    `toml:"traffic_cap_gb"`  // месячный лимит; 0 = без лимита
	Secret        string `toml:"secret"`          // REST-auth секрет (генерируется в setup)
	PublicIP      string `toml:"public_ip"`       // публичный IP для relay-адреса (обязателен за NAT)
	TLSCert       string `toml:"tls_cert"`        // путь к сертификату для TURN-over-TLS (опц.; иначе self-signed)
	TLSKey        string `toml:"tls_key"`         // путь к ключу
}

// Publish — как демон попадает в пул.
//
//	auto    — автопубликация на Origin.Control, сервер раздаёт мостик по когортам;
//	private — сервер о демоне не знает, ссылка печатается командой `link` и раздаётся из рук в руки.
//
// EnrollSecret — общий секрет регистрации; барьер от случайного спама пула. Вшит в демон
// (DefaultEnrollSecret) и спрашиваться не должен: волонтёру неоткуда его узнать. PublicHost —
// публичный хост/IP для регистрации; если пуст, сервер берёт IP запроса.
type Publish struct {
	Mode         string `toml:"mode"`
	EnrollSecret string `toml:"enroll_secret"`
	PublicHost   string `toml:"public_host"`
}

// Operator — необязательный контакт оператора (для связи, не публикуется пользователям).
type Operator struct {
	Contact string `toml:"contact"`
}

const (
	ModeAuto    = "auto"
	ModePrivate = "private"
)

// DefaultEnrollSecret — общий секрет регистрации в пуле, вшитый в демон. Он лежит в открытом коде,
// и это осознанно: секрет не защищает от целевой атаки, а лишь отсекает случайный спам в пул.
// Настоящая защита выдачи мостиков — на стороне сервера (вход через Google/Apple + когорты).
// Должен совпадать с RELAY_ENROLL_SECRET в .env бэкенда; менять — одновременно в обоих местах
// (старые демоны со старым значением перестанут регистрироваться).
const DefaultEnrollSecret = "d813a45b5eee8c0a64f74104d788feb95f2ec4c707614255"

// Default возвращает конфиг со значениями по умолчанию (до заполнения мастером).
func Default() Config {
	return Config{
		Origin: Origin{
			URL:     "https://api.ringlyapp.net",
			Control: "https://api.ringlyapp.net",
		},
		Bridge: Bridge{
			Enabled:     true,
			Listen:      ":443",
			SingboxPath: "sing-box",
		},
		Reality: Reality{
			SNI: "www.microsoft.com",
		},
		Calls: Calls{
			Enabled:       false,
			TurnListen:    ":3478",
			TurnTLSListen: ":5349",
			TrafficCapGB:  100,
		},
		Publish: Publish{Mode: ModeAuto, EnrollSecret: DefaultEnrollSecret},
	}
}

// Load читает конфиг из файла.
func Load(path string) (Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return Config{}, fmt.Errorf("чтение конфига %s: %w", path, err)
	}
	// конфиги, созданные до того, как секрет вшили, — без него; руками его никто не впишет
	if c.Publish.EnrollSecret == "" {
		c.Publish.EnrollSecret = DefaultEnrollSecret
	}
	return c, nil
}

// Save атомарно записывает конфиг: сначала во временный файл рядом, затем rename — чтобы прерывание
// записи не оставило половинчатый конфиг, по которому демон не поднимется.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("создание каталога конфига: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("временный файл конфига: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // на случай ошибки до rename

	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		tmp.Close()
		return fmt.Errorf("сериализация конфига: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие временного конфига: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil { // приватный ключ Reality — только владельцу
		return fmt.Errorf("права на конфиг: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("замена конфига: %w", err)
	}
	return nil
}

// PortFromListen извлекает номер порта из строки вида ":443" или "0.0.0.0:443".
func PortFromListen(listen string) (int, error) {
	l := listen
	if strings.HasPrefix(l, ":") {
		l = "0.0.0.0" + l
	}
	_, portStr, err := net.SplitHostPort(l)
	if err != nil {
		return 0, fmt.Errorf("не разобрать порт из %q: %w", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("порт %q не число: %w", portStr, err)
	}
	return port, nil
}

// Validate проверяет минимальную непротиворечивость конфига перед запуском.
func (c Config) Validate() error {
	if !c.Bridge.Enabled && !c.Calls.Enabled {
		return fmt.Errorf("не включён ни один модуль (bridge или calls) — демону нечего делать")
	}
	if c.Bridge.Enabled {
		if c.Origin.URL == "" {
			return fmt.Errorf("bridge включён, но origin.url пуст")
		}
		if err := ValidateURL(c.Origin.URL); err != nil {
			return fmt.Errorf("origin.url: %w", err)
		}
		if c.Reality.PrivateKey == "" || c.Reality.ShortID == "" || c.Reality.UUID == "" {
			return fmt.Errorf("bridge включён, но ключи Reality не заданы — запустите `ringly-relay setup`")
		}
		if err := validatePrivateKey(c.Reality.PrivateKey); err != nil {
			return err
		}
		if !shortIDRe.MatchString(c.Reality.ShortID) {
			return fmt.Errorf("reality.short_id %q некорректен (hex чётной длины, до 16 символов) — удалите ключи и запустите `ringly-relay setup`", c.Reality.ShortID)
		}
		if !uuidRe.MatchString(c.Reality.UUID) {
			return fmt.Errorf("reality.uuid %q некорректен — удалите ключи и запустите `ringly-relay setup`", c.Reality.UUID)
		}
		if c.Reality.SNI == "" {
			return fmt.Errorf("bridge включён, но reality.sni пуст")
		}
		if err := ValidateHost(c.Reality.SNI); err != nil {
			return fmt.Errorf("reality.sni: %w", err)
		}
		if err := validateListen("bridge.listen", c.Bridge.Listen); err != nil {
			return err
		}
	}
	if c.Calls.Enabled {
		if c.Calls.Secret == "" {
			return fmt.Errorf("calls включён, но secret не задан — запустите `ringly-relay setup`")
		}
		if c.Calls.PublicIP == "" {
			return fmt.Errorf("calls включён, но public_ip не задан — за NAT relay без него не работает")
		}
		if net.ParseIP(c.Calls.PublicIP) == nil {
			return fmt.Errorf("calls.public_ip %q — нужен IP-адрес, например 203.0.113.10", c.Calls.PublicIP)
		}
		if err := validateListen("calls.turn_listen", c.Calls.TurnListen); err != nil {
			return err
		}
		if (c.Calls.TLSCert == "") != (c.Calls.TLSKey == "") {
			return fmt.Errorf("для TURN-over-TLS нужны и tls_cert, и tls_key (или ни одного — тогда self-signed)")
		}
	}
	switch c.Publish.Mode {
	case ModeAuto:
		if c.Origin.Control == "" {
			return fmt.Errorf("publish.mode=auto, но origin.control пуст")
		}
		if err := ValidateURL(c.Origin.Control); err != nil {
			return fmt.Errorf("origin.control: %w", err)
		}
		if c.Publish.EnrollSecret == "" {
			return fmt.Errorf("publish.mode=auto, но publish.enroll_secret пуст — задайте секрет регистрации")
		}
	case ModePrivate:
		// ок — регистрации нет, мостик раздаётся ссылкой
	default:
		return fmt.Errorf("неизвестный publish.mode=%q (ожидается auto|private)", c.Publish.Mode)
	}
	return nil
}

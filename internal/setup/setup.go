// Package setup — интерактивный мастер первоначальной настройки демона.
//
// Спрашивает по одному тумблеру с объяснением последствий, генерирует ключи Reality и пишет конфиг.
// Ретранслятор звонков выключен по умолчанию сознательно: он качает через себя весь голос/видео,
// а мостик сообщений почти ничего не потребляет — доброволец должен включить звонки осознанно.
//
// Принцип: человек не должен править конфиг руками. Поэтому мастер не спрашивает то, на что у
// новичка нет ответа (адреса origin/control — у них верные значения по умолчанию), сам подставляет
// публичный IP, чистит следы копирования (экранирование и разметку Markdown) и проверяет каждое
// поле сразу — с повторным вопросом, а не ошибкой в конце или падением sing-box при запуске.
package setup

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/netcheck"
)

// Проверка домена Reality с сервера. Переменная — чтобы тесты мастера не ходили в сеть.
var checkRealityTarget = func(host string) error {
	_, err := netcheck.RealityTarget(host, 5*time.Second)
	return err
}

// Определение публичного адреса. Переменная — по той же причине.
var detectPublicIP = netcheck.PublicIP

// Run проводит мастер, читая ответы из in и печатая в out, и возвращает готовый конфиг.
func Run(in io.Reader, out io.Writer, existing *config.Config) (config.Config, error) {
	r := bufio.NewReader(in)
	p := &prompt{r: r, out: out}

	c := config.Default()
	if existing != nil {
		c = *existing // повторный запуск правит существующий конфиг
	}

	fmt.Fprintln(out, "Настройка Ringly Relay")
	fmt.Fprintln(out, "Доброволец-ретранслятор помогает пользователям обходить блокировки.")
	fmt.Fprintln(out, "Трафик сквозь вас зашифрован — прочитать его нельзя.")
	fmt.Fprintln(out, strings.Repeat("─", 60))

	// Адреса сервера Ringly не спрашиваем: у них верные значения по умолчанию, а новичок их не знает.
	// Если в старом конфиге они испорчены копированием — чиним молча.
	c.Origin.URL = config.NormalizeURL(c.Origin.URL)
	c.Origin.Control = config.NormalizeURL(firstNonEmpty(c.Origin.Control, c.Origin.URL))

	publicIP, ipErr := detectPublicIP()

	// --- Мостик сообщений ---
	fmt.Fprintln(out, "\n[1/3] Мостик сообщений — пересылает зашифрованный трафик приложения.")
	fmt.Fprintln(out, "      Почти не расходует трафик. Рекомендуется включить.")
	c.Bridge.Enabled = p.yesNo("Включить мостик сообщений?", c.Bridge.Enabled)
	if c.Bridge.Enabled {
		c.Bridge.Listen = p.valid("  Порт входа (443 = маскировка под HTTPS)", c.Bridge.Listen, normalizeListen, validListen)
		fmt.Fprintln(out, "  Домен для маскировки: мостик выдаёт себя за этот сайт. Лучше крупный сайт,")
		fmt.Fprintln(out, "  который хостится недалеко от вашего сервера. Сайт должен открываться с сервера.")
		for {
			c.Reality.SNI = p.valid("  Домен", config.NormalizeHost(c.Reality.SNI), config.NormalizeHost, config.ValidateHost)
			fmt.Fprintf(out, "  проверяю %s с этого сервера…\n", c.Reality.SNI)
			if err := checkRealityTarget(c.Reality.SNI); err != nil {
				fmt.Fprintf(out, "  ⚠️ %v\n", err)
				if !p.yesNo("  Оставить этот домен всё равно?", false) {
					continue
				}
			} else {
				fmt.Fprintln(out, "  ✓ домен подходит")
			}
			break
		}
	}

	// --- Ретранслятор звонков ---
	fmt.Fprintln(out, "\n[2/3] Ретранслятор звонков (TURN) — пропускает через себя голос и видео.")
	fmt.Fprintln(out, "      ⚠️ Это РЕАЛЬНЫЙ трафик (десятки–сотни МБ за звонок). По умолчанию выключено.")
	c.Calls.Enabled = p.yesNo("Включить ретранслятор звонков?", c.Calls.Enabled)
	if c.Calls.Enabled {
		c.Calls.TurnListen = p.text("  TURN-порт (UDP+TCP)", c.Calls.TurnListen)
		c.Calls.TurnTLSListen = p.text("  TURN-over-TLS порт (фолбэк под DPI на UDP)", c.Calls.TurnTLSListen)
		c.Calls.TrafficCapGB = p.intVal("  Месячный лимит трафика, ГБ (0 = без лимита)", c.Calls.TrafficCapGB)
		fmt.Fprintln(out, "  Публичный IP обязателен: без него relay раздаётся на внутренний адрес.")
		if ipErr != nil {
			fmt.Fprintf(out, "  ⚠️ %v\n", ipErr)
		}
		c.Calls.PublicIP = p.valid("  Публичный IP этого сервера", firstNonEmpty(c.Calls.PublicIP, publicIP), strings.TrimSpace, validIP)
	}

	// --- Режим публикации ---
	fmt.Fprintln(out, "\n[3/3] Режим публикации:")
	fmt.Fprintln(out, "      auto    — сервер узнаёт о вас и раздаёт мостик пользователям по когортам")
	fmt.Fprintln(out, "      private — сервер о вас не знает; ссылку раздаёте сами (`ringly-relay link`)")
	c.Publish.Mode = p.choice("Режим публикации", []string{config.ModeAuto, config.ModePrivate}, c.Publish.Mode)
	// секрет регистрации вшит в демон (config.DefaultEnrollSecret) — не спрашиваем
	if c.Publish.Mode == config.ModeAuto && c.Publish.EnrollSecret == "" {
		c.Publish.EnrollSecret = config.DefaultEnrollSecret
	}
	// Адрес нужен в обоих режимах: в private он попадает в ссылку, в auto — в регистрацию. «Пусть
	// сервер определит сам» не годится: за Nginx сервер видит адрес прокси, а не этого VPS.
	if ipErr != nil {
		fmt.Fprintf(out, "  ⚠️ %v\n", ipErr)
	}
	c.Publish.PublicHost = p.valid("  Публичный адрес этого сервера (IP или домен)",
		firstNonEmpty(c.Publish.PublicHost, publicIP), config.NormalizeHost, validHostOrIP)

	// --- Ключи Reality ---
	if c.Bridge.Enabled && c.Reality.PrivateKey == "" {
		keys, err := bridgelink.GenerateKeys()
		if err != nil {
			return config.Config{}, err
		}
		c.Reality.PrivateKey = keys.PrivateKey
		c.Reality.ShortID = keys.ShortID
		c.Reality.UUID = keys.UUID
		fmt.Fprintln(out, "\nСгенерированы ключи Reality и UUID.")
	}
	if c.Calls.Enabled && c.Calls.Secret == "" {
		secret, err := bridgelink.GenerateSecret()
		if err != nil {
			return config.Config{}, err
		}
		c.Calls.Secret = secret
		fmt.Fprintln(out, "Сгенерирован секрет TURN.")
	}

	if err := c.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("итоговый конфиг некорректен: %w", err)
	}
	return c, nil
}

// prompt — маленький помощник для чтения ответов с дефолтами.
type prompt struct {
	r   *bufio.Reader
	out io.Writer
}

func (p *prompt) line(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	s, _ := p.r.ReadString('\n')
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

func (p *prompt) text(label, def string) string { return p.line(label, def) }

// valid спрашивает, нормализует ответ и переспрашивает, пока проверка не пройдёт.
func (p *prompt) valid(label, def string, normalize func(string) string, check func(string) error) string {
	for {
		s := normalize(p.line(label, def))
		if err := check(s); err != nil {
			fmt.Fprintf(p.out, "  %v\n", err)
			continue
		}
		return s
	}
}

func normalizeListen(s string) string {
	s = strings.TrimSpace(s)
	if _, err := strconv.Atoi(s); err == nil {
		return ":" + s // «443» → «:443»
	}
	return s
}

func validListen(s string) error {
	port, err := config.PortFromListen(s)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("нужен номер порта, например 443")
	}
	return nil
}

func validIP(s string) error {
	if net.ParseIP(s) == nil {
		return fmt.Errorf("нужен IP-адрес, например 203.0.113.10")
	}
	return nil
}

func validHostOrIP(s string) error {
	if net.ParseIP(s) != nil {
		return nil
	}
	return config.ValidateHost(s)
}

func (p *prompt) yesNo(label string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	for {
		s := strings.ToLower(p.line(label+" (y/n)", d))
		switch s {
		case "y", "yes", "д", "да":
			return true
		case "n", "no", "н", "нет":
			return false
		}
		fmt.Fprintln(p.out, "  ответьте y или n")
	}
}

func (p *prompt) intVal(label string, def int) int {
	for {
		s := p.line(label, strconv.Itoa(def))
		n, err := strconv.Atoi(s)
		if err == nil && n >= 0 {
			return n
		}
		fmt.Fprintln(p.out, "  введите неотрицательное число")
	}
}

func (p *prompt) choice(label string, opts []string, def string) string {
	for {
		s := strings.ToLower(p.line(fmt.Sprintf("%s (%s)", label, strings.Join(opts, "/")), def))
		for _, o := range opts {
			if s == o {
				return o
			}
		}
		fmt.Fprintf(p.out, "  выберите один из: %s\n", strings.Join(opts, ", "))
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

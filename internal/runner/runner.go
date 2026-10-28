// Package runner запускает включённые модули и держит демон живым до сигнала остановки.
//
// Каждый модуль (мостик сообщений, TURN, публикатор) реализует Start/Stop; runner поднимает их,
// корректно гасит по SIGINT/SIGTERM и завершается с ошибкой, если модуль упал после старта.
package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ringlymessenger/ringly-relay/internal/bridge"
	"github.com/ringlymessenger/ringly-relay/internal/calls"
	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/link"
	"github.com/ringlymessenger/ringly-relay/internal/publisher"
	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

// Module — сетевой компонент демона (мостик сообщений, TURN, публикатор). Реализации появятся
// на следующих этапах; runner уже умеет их поднимать и останавливать.
type Module interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Watched — модуль, который может упасть уже после старта (мостик супервизирует sing-box).
// Падение завершает весь демон с ошибкой: перезапуск — забота Docker/systemd. Раньше демон
// продолжал жить с мёртвым портом, и супервизор не видел повода его перезапускать.
type Watched interface {
	Failed() <-chan error
}

// Run поднимает демон по конфигу и блокируется до сигнала остановки.
func Run(c config.Config, out io.Writer) error {
	if err := c.Validate(); err != nil {
		return err
	}

	printPlan(c, out)

	mods := build(c, out)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	started := make([]Module, 0, len(mods))
	for _, m := range mods {
		fmt.Fprintf(out, "запуск модуля: %s\n", m.Name())
		if err := m.Start(ctx); err != nil {
			shutdown(started, out)
			return fmt.Errorf("модуль %s не стартовал: %w", m.Name(), err)
		}
		started = append(started, m)
	}

	if len(started) == 0 {
		fmt.Fprintln(out, "⚠️  включённые модули не поднялись. Демон ждёт сигнал.")
	}
	fmt.Fprintln(out, "готов. Ctrl+C для остановки.")
	printLink(c, out)

	failed := make(chan error, len(started))
	for _, m := range started {
		if w, ok := m.(Watched); ok {
			go func(name string, ch <-chan error) {
				if err, ok := <-ch; ok && err != nil {
					failed <- fmt.Errorf("модуль %s упал: %w", name, err)
				}
			}(m.Name(), w.Failed())
		}
	}

	select {
	case <-ctx.Done():
		fmt.Fprintln(out, "\nостановка…")
		shutdown(started, out)
		return nil
	case err := <-failed:
		fmt.Fprintf(out, "⚠️ %v\nостановка, перезапуск — за Docker/systemd\n", err)
		shutdown(started, out)
		return err
	}
}

// printLink — в приватном режиме ссылку некому раздать, кроме оператора: печатаем её сразу, чтобы
// её было видно в логах (`docker compose logs relay`) без отдельной команды.
func printLink(c config.Config, out io.Writer) {
	if !c.Bridge.Enabled || c.Publish.Mode != config.ModePrivate {
		return
	}
	if err := link.Print(c, "", out); err != nil {
		fmt.Fprintf(out, "ссылку-мостик не напечатать: %v\n", err)
	}
}

// build собирает список модулей по конфигу.
func build(c config.Config, out io.Writer) []Module {
	// Счётчики трафика нужны, только если есть кому их отправить (автопубликация на сервер).
	var counters *stats.Counters
	if c.Publish.Mode == config.ModeAuto {
		counters = stats.New()
	}
	var mods []Module
	if c.Bridge.Enabled {
		mods = append(mods, bridge.New(c, out, counters))
	}
	if c.Calls.Enabled {
		mods = append(mods, calls.New(c, out, counters))
	}
	if c.Publish.Mode == config.ModeAuto {
		mods = append(mods, publisher.New(c, out, counters))
	}
	return mods
}

func shutdown(mods []Module, out io.Writer) {
	ctx := context.Background()
	// гасим в обратном порядке запуска
	for i := len(mods) - 1; i >= 0; i-- {
		if err := mods[i].Stop(ctx); err != nil {
			fmt.Fprintf(out, "модуль %s остановлен с ошибкой: %v\n", mods[i].Name(), err)
		}
	}
}

func printPlan(c config.Config, out io.Writer) {
	fmt.Fprintln(out, "План запуска:")
	if c.Bridge.Enabled {
		fmt.Fprintf(out, "  • мостик сообщений: вход %s (Reality, SNI %s) → %s\n",
			c.Bridge.Listen, c.Reality.SNI, c.Origin.URL)
	} else {
		fmt.Fprintln(out, "  • мостик сообщений: выключен")
	}
	if c.Calls.Enabled {
		cap := "без лимита"
		if c.Calls.TrafficCapGB > 0 {
			cap = fmt.Sprintf("%d ГБ/мес", c.Calls.TrafficCapGB)
		}
		fmt.Fprintf(out, "  • звонки (TURN): UDP/TCP %s, TLS %s, лимит %s\n",
			c.Calls.TurnListen, c.Calls.TurnTLSListen, cap)
	} else {
		fmt.Fprintln(out, "  • звонки (TURN): выключены")
	}
	fmt.Fprintf(out, "  • публикация: %s\n", c.Publish.Mode)
}

// ExitOnError печатает ошибку в stderr и выходит с кодом 1 (утилита для main).
func ExitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

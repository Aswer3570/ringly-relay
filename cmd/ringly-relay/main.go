// Command ringly-relay — демон-ретранслятор Ringly для обхода блокировок.
//
// Подкоманды:
//
//	setup  — интерактивная настройка, пишет конфиг
//	run    — запуск по конфигу (под systemd/Docker)
//	status — что настроено сейчас
//	doctor — проверка установки по пунктам (sing-box, порт, домен маскировки, сервер Ringly)
//	link   — печать ссылки-мостика для приватного режима
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/doctor"
	"github.com/ringlymessenger/ringly-relay/internal/link"
	"github.com/ringlymessenger/ringly-relay/internal/runner"
	"github.com/ringlymessenger/ringly-relay/internal/setup"
	"github.com/ringlymessenger/ringly-relay/internal/status"
)

// version подставляется линкером при сборке (см. Makefile): -X main.version=...
var version = "dev"

func main() {
	var cfgPath string

	root := &cobra.Command{
		Use:           "ringly-relay",
		Short:         "Демон-ретранслятор Ringly (обход блокировок)",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&cfgPath, "config", "c", config.DefaultPath, "путь к конфигу")

	root.AddCommand(
		setupCmd(&cfgPath),
		runCmd(&cfgPath),
		statusCmd(&cfgPath),
		doctorCmd(&cfgPath),
		linkCmd(&cfgPath),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func setupCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Интерактивная настройка демона",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var existing *config.Config
			if c, err := config.Load(*cfgPath); err == nil {
				existing = &c // повторный запуск правит существующий конфиг
			}
			c, err := setup.Run(os.Stdin, os.Stdout, existing)
			if err != nil {
				return err
			}
			if err := config.Save(*cfgPath, c); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "\nКонфиг сохранён: %s\n", *cfgPath)
			fmt.Fprintln(os.Stdout, "Дальше: запустите демон (Docker: docker compose up -d; systemd: systemctl restart ringly-relay)")
			fmt.Fprintln(os.Stdout, "и проверьте установку: ringly-relay doctor")
			return nil
		},
	}
}

func runCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Запустить демон по конфигу",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nсначала выполните: ringly-relay setup", err)
			}
			c.StatePath = statePath(*cfgPath)
			return runner.Run(c, os.Stdout)
		},
	}
}

func statusCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Показать текущую конфигурацию",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nсначала выполните: ringly-relay setup", err)
			}
			status.Print(c, os.Stdout)
			return nil
		},
	}
}

// statePath — где демон хранит своё состояние (токен в пуле). По умолчанию рядом с конфигом: в Docker
// этот каталог доступен на запись. Под systemd /etc/ringly-relay только для чтения, поэтому юнит
// задаёт RINGLY_RELAY_STATE_DIR=/var/lib/ringly-relay (StateDirectory).
func statePath(cfgPath string) string {
	if dir := os.Getenv("RINGLY_RELAY_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "state.json")
	}
	return filepath.Join(filepath.Dir(cfgPath), "state.json")
}

func doctorCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Проверить установку по пунктам",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nсначала выполните: ringly-relay setup", err)
			}
			if n := doctor.Run(c, os.Stdout); n > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
}

func linkCmd(cfgPath *string) *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Напечатать ссылку-мостик (приватный режим)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nсначала выполните: ringly-relay setup", err)
			}
			return link.Print(c, host, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "публичный IP или домен демона (по умолчанию — из конфига или определяется сам)")
	return cmd
}

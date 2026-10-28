// Command ringly-relay — the Ringly relay daemon for getting around blocking.
//
// Subcommands:
//
//	setup  — interactive setup, writes the configuration
//	run    — run with the configuration (under systemd/Docker)
//	status — what is configured now
//	doctor — step-by-step installation check (sing-box, port, cover domain, Ringly server)
//	link   — print the bridge link for private mode
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

// version is set by the linker at build time (see Makefile): -X main.version=...
var version = "dev"

func main() {
	var cfgPath string

	root := &cobra.Command{
		Use:           "ringly-relay",
		Short:         "Ringly relay daemon (getting around blocking)",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&cfgPath, "config", "c", config.DefaultPath, "configuration path")

	root.AddCommand(
		setupCmd(&cfgPath),
		runCmd(&cfgPath),
		statusCmd(&cfgPath),
		doctorCmd(&cfgPath),
		linkCmd(&cfgPath),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func setupCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive daemon setup",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var existing *config.Config
			if c, err := config.Load(*cfgPath); err == nil {
				existing = &c // running again edits the existing configuration
			}
			c, err := setup.Run(os.Stdin, os.Stdout, existing)
			if err != nil {
				return err
			}
			if err := config.Save(*cfgPath, c); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "\nConfiguration saved: %s\n", *cfgPath)
			fmt.Fprintln(os.Stdout, "Next: start the daemon (Docker: docker compose up -d; systemd: systemctl restart ringly-relay)")
			fmt.Fprintln(os.Stdout, "and check the installation: ringly-relay doctor")
			return nil
		},
	}
}

func runCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon with the configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nrun first: ringly-relay setup", err)
			}
			c.StatePath = statePath(*cfgPath)
			return runner.Run(c, os.Stdout)
		},
	}
}

func statusCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nrun first: ringly-relay setup", err)
			}
			status.Print(c, os.Stdout)
			return nil
		},
	}
}

// statePath — where the daemon keeps its state (its token in the pool). By default next to the
// configuration: in Docker that directory is writable. Under systemd /etc/ringly-relay is read-only, so
// the unit sets RINGLY_RELAY_STATE_DIR=/var/lib/ringly-relay (StateDirectory).
func statePath(cfgPath string) string {
	if dir := os.Getenv("RINGLY_RELAY_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "state.json")
	}
	return filepath.Join(filepath.Dir(cfgPath), "state.json")
}

func doctorCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the installation step by step",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nrun first: ringly-relay setup", err)
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
		Short: "Print the bridge link (private mode)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*cfgPath)
			if err != nil {
				return fmt.Errorf("%w\nrun first: ringly-relay setup", err)
			}
			return link.Print(c, host, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "the daemon's public IP or domain (default: from the configuration or detected)")
	return cmd
}

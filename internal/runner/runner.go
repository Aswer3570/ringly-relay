// Package runner starts the enabled modules and keeps the daemon alive until a stop signal.
//
// Each module (message bridge, TURN, publisher) implements Start/Stop; the runner starts them, shuts
// them down cleanly on SIGINT/SIGTERM and exits with an error if a module fails after startup.
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

// Module — a network component of the daemon (message bridge, TURN, publisher).
type Module interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Watched — a module that can fail after startup (the bridge supervises sing-box). A failure exits the
// whole daemon with an error: restarting is Docker's/systemd's job. The daemon used to keep living with
// a dead port, and the supervisor saw no reason to restart it.
type Watched interface {
	Failed() <-chan error
}

// Run starts the daemon from the configuration and blocks until a stop signal.
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
		fmt.Fprintf(out, "starting module: %s\n", m.Name())
		if err := m.Start(ctx); err != nil {
			shutdown(started, out)
			return fmt.Errorf("module %s failed to start: %w", m.Name(), err)
		}
		started = append(started, m)
	}

	if len(started) == 0 {
		fmt.Fprintln(out, "⚠️  the enabled modules did not start. The daemon is waiting for a signal.")
	}
	fmt.Fprintln(out, "ready. Ctrl+C to stop.")
	printLink(c, out)

	failed := make(chan error, len(started))
	for _, m := range started {
		if w, ok := m.(Watched); ok {
			go func(name string, ch <-chan error) {
				if err, ok := <-ch; ok && err != nil {
					failed <- fmt.Errorf("module %s failed: %w", name, err)
				}
			}(m.Name(), w.Failed())
		}
	}

	select {
	case <-ctx.Done():
		fmt.Fprintln(out, "\nstopping…")
		shutdown(started, out)
		return nil
	case err := <-failed:
		fmt.Fprintf(out, "⚠️ %v\nstopping; Docker/systemd will restart the daemon\n", err)
		shutdown(started, out)
		return err
	}
}

// printLink — in private mode only the operator can share the link, so print it right away to make it
// visible in the logs (`docker compose logs relay`) without a separate command.
func printLink(c config.Config, out io.Writer) {
	if !c.Bridge.Enabled || c.Publish.Mode != config.ModePrivate {
		return
	}
	if err := link.Print(c, "", out); err != nil {
		fmt.Fprintf(out, "could not print the bridge link: %v\n", err)
	}
}

// build assembles the list of modules from the configuration.
func build(c config.Config, out io.Writer) []Module {
	// Traffic counters are needed only if there is someone to send them to (auto-publishing).
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
	// stop in reverse start order
	for i := len(mods) - 1; i >= 0; i-- {
		if err := mods[i].Stop(ctx); err != nil {
			fmt.Fprintf(out, "module %s stopped with an error: %v\n", mods[i].Name(), err)
		}
	}
}

func printPlan(c config.Config, out io.Writer) {
	fmt.Fprintln(out, "Start plan:")
	if c.Bridge.Enabled {
		fmt.Fprintf(out, "  • message bridge: entry %s (Reality, SNI %s) → %s\n",
			c.Bridge.Listen, c.Reality.SNI, c.Origin.URL)
	} else {
		fmt.Fprintln(out, "  • message bridge: off")
	}
	if c.Calls.Enabled {
		cap := "unlimited"
		if c.Calls.TrafficCapGB > 0 {
			cap = fmt.Sprintf("%d GB/month", c.Calls.TrafficCapGB)
		}
		fmt.Fprintf(out, "  • calls (TURN): UDP/TCP %s, TLS %s, limit %s\n",
			c.Calls.TurnListen, c.Calls.TurnTLSListen, cap)
	} else {
		fmt.Fprintln(out, "  • calls (TURN): off")
	}
	fmt.Fprintf(out, "  • publishing: %s\n", c.Publish.Mode)
}

// ExitOnError prints the error to stderr and exits with code 1 (a helper for main).
func ExitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

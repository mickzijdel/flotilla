package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mickzijdel/flotilla/internal/daemon"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

// scanInterval is the supervisor's poll cadence (status files + events + re-exec).
const scanInterval = 2 * time.Second

// osExecutable is overridable in tests.
var osExecutable = os.Executable

func currentExe() string {
	if exe, err := osExecutable(); err == nil {
		return exe
	}
	return "flotilla"
}

func daemonCmd(f *fleet.Fleet) *cobra.Command {
	c := &cobra.Command{Use: "daemon", Short: "Run the optional supervisor (auto-submit + inbox)"}
	c.AddCommand(daemonStartCmd(), daemonStopCmd(), daemonStatusCmd(), daemonRunCmd(f))
	return c
}

func daemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := daemon.DefaultPaths()
			if daemon.IsRunning(p) {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon already running")
				return err
			}
			if err := daemon.Start(p, currentExe()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon started")
			return err
		},
	}
}

func daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := daemon.StopDaemon(daemon.DefaultPaths(), 5*time.Second); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			return err
		},
	}
}

func daemonStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := daemon.ReadStatus(daemon.DefaultPaths(), 5)
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
			}
			out := cmd.OutOrStdout()
			if !st.Running {
				_, err := fmt.Fprintln(out, "daemon: not running")
				return err
			}
			if _, err := fmt.Fprintf(out, "daemon: running (pid %d), %d watched agent(s)\n", st.PID, st.WatchedAgents); err != nil {
				return err
			}
			for _, e := range st.Recent {
				if _, err := fmt.Fprintf(out, "  %s  %s  %s\n", e.TS.Format(time.RFC3339), e.Agent, e.Type); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

func daemonRunCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:    "run",
		Short:  "Run the daemon in the foreground (for systemd)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := daemon.DefaultPaths()
			sup := &daemon.Supervisor{
				Fleet:    f,
				Backend:  f.Backend,
				Paths:    p,
				Registry: daemon.NewRegistry(),
			}
			return daemon.RunForeground(cmd.Context(), sup, p, currentExe(), scanInterval)
		},
	}
}

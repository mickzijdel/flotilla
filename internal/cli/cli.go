package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/mickzijdel/flotilla/internal/forge"
	"github.com/mickzijdel/flotilla/internal/preflight"
	"github.com/spf13/cobra"
)

// BuildRoot wires the CLI against a Fleet.
func BuildRoot(f *fleet.Fleet) *cobra.Command {
	root := &cobra.Command{Use: "flotilla", Short: "Manage a fleet of autonomous coding agents"}
	root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), submitCmd(f), logsCmd(f), agentsCmd(), doctorCmd())
	return root
}

func spawnCmd(f *fleet.Fleet) *cobra.Command {
	var agentName, prompt string
	var noFirewall bool
	c := &cobra.Command{
		Use:   "spawn <repo>",
		Short: "Clone a repo and start an agent on it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if noFirewall {
				f.EgressFirewall = false
			}
			if rep := preflight.Check(cmd.Context(), preflight.Real()); !rep.OK() {
				return fmt.Errorf("preflight failed (run 'flotilla doctor'): %s", strings.Join(rep.Messages(), "; "))
			}
			builtins, err := agent.Builtins()
			if err != nil {
				return err
			}
			prof, ok := builtins[agentName]
			if !ok {
				return fmt.Errorf("unknown agent %q (try: flotilla agents)", agentName)
			}
			a, err := f.Spawn(cmd.Context(), args[0], prof, prompt)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", a.Name, a.Status, a.ID)
			return err
		},
	}
	c.Flags().StringVar(&agentName, "agent", "claude", "agent profile to run")
	c.Flags().StringVar(&prompt, "prompt", "", "task prompt for the agent")
	c.Flags().BoolVar(&noFirewall, "no-egress-firewall", false, "disable the default-deny egress firewall (trusted/dev runs)")
	return c
}

func listCmd(f *fleet.Fleet) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the fleet",
		RunE: func(cmd *cobra.Command, _ []string) error {
			agents, err := f.List(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				return enc.Encode(agents)
			}
			for _, a := range agents {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", a.Name, a.Status, a.Repo); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

func attachCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <agent>",
		Short: "Print attach info for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := f.Attach(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), info.DockerExec); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), info.VSCode)
			return err
		},
	}
}

func stopCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <agent>",
		Short: "Stop an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.Stop(cmd.Context(), args[0])
		},
	}
}

func rmCmd(f *fleet.Fleet) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <agent>",
		Short: "Remove an agent's container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.Remove(cmd.Context(), args[0])
		},
	}
}

func submitCmd(f *fleet.Fleet) *cobra.Command {
	var force, asJSON bool
	c := &cobra.Command{
		Use:   "submit <agent>",
		Short: "Push the agent's commits and open/update a PR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sub, err := f.Submit(cmd.Context(), args[0], force)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(sub)
			}
			out := cmd.OutOrStdout()
			switch {
			case sub.PushOnly:
				// A non-GitHub remote yields no compare URL; don't print a dangling "→ open a PR: ".
				line := fmt.Sprintf("Pushed %s → open a PR: %s\n", sub.Branch, sub.PRURL)
				if sub.PRURL == "" {
					line = fmt.Sprintf("Pushed %s — open a pull request on your host to merge it\n", sub.Branch)
				}
				if _, err := fmt.Fprint(out, line); err != nil {
					return err
				}
				if sub.Note != "" {
					_, err = fmt.Fprintf(out, "(note: %s)\n", sub.Note)
				}
				return err
			case sub.Created:
				_, err = fmt.Fprintf(out, "Pushed %s → opened PR %s\n", sub.Branch, sub.PRURL)
				return err
			default:
				_, err = fmt.Fprintf(out, "Pushed %s → updated existing PR %s\n", sub.Branch, sub.PRURL)
				return err
			}
		},
	}
	c.Flags().BoolVar(&force, "force", false, "submit even if the agent is still running")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check prerequisites (docker, docker daemon, devcontainer CLI)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep := preflight.Check(cmd.Context(), preflight.Real())
			for _, m := range rep.Messages() {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), m); err != nil {
					return err
				}
			}
			if forge.GHAvailable(cmd.Context()) {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "ok: gh CLI authenticated (PRs will be opened automatically)"); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "advisory: gh CLI not found/authenticated — submit will push only and print a compare URL"); err != nil {
					return err
				}
			}
			if !rep.OK() {
				return fmt.Errorf("missing prerequisites")
			}
			return nil
		},
	}
}

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List available agent profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			builtins, err := agent.Builtins()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(builtins))
			for n := range builtins {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), n); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

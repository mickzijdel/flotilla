package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mickzijdel/flotilla/internal/agent"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

// BuildRoot wires the CLI against a Fleet.
func BuildRoot(f *fleet.Fleet) *cobra.Command {
	root := &cobra.Command{Use: "flotilla", Short: "Manage a fleet of autonomous coding agents"}
	root.AddCommand(spawnCmd(f), listCmd(f), attachCmd(f), stopCmd(f), rmCmd(f), agentsCmd())
	return root
}

func spawnCmd(f *fleet.Fleet) *cobra.Command {
	var agentName, prompt string
	c := &cobra.Command{
		Use:   "spawn <repo>",
		Short: "Clone a repo and start an agent on it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

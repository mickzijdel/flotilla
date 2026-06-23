package cli

import (
	"encoding/json"
	"fmt"

	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func fetchCmd(f *fleet.Fleet) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "fetch <agent>",
		Short: "Re-fetch origin into a running agent's clone (it has no git credentials)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := f.Fetch(cmd.Context(), name); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"agent": name, "fetched": true,
				})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Fetched origin for %s\n", name)
			return err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func answerCmd(f *fleet.Fleet) *cobra.Command {
	var id string
	var asJSON bool
	c := &cobra.Command{
		Use:   "answer <agent> <text>",
		Short: "Answer a running agent's pending question (unblocks it)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			text := strings.Join(args[1:], " ")
			if err := f.Answer(cmd.Context(), name, id, text); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"agent": name, "answered": true,
				})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Answered %s\n", name)
			return err
		},
	}
	c.Flags().StringVar(&id, "id", "", "question id to answer (required only when several are pending)")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return c
}

package cli

import (
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

// BuildRoot is fully implemented in Task 11.
func BuildRoot(_ *fleet.Fleet) *cobra.Command {
	return &cobra.Command{Use: "flotilla", Short: "Manage a fleet of autonomous coding agents"}
}

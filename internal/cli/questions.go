package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func questionsCmd(f *fleet.Fleet) *cobra.Command {
	var asJSON, watch bool
	c := &cobra.Command{
		Use:   "questions",
		Short: "List pending agent questions awaiting an operator answer",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if watch {
				return watchQuestions(cmd.Context(), f, cmd.OutOrStdout())
			}
			qs, err := f.Questions(cmd.Context())
			if err != nil {
				return err
			}
			return printQuestions(cmd.OutOrStdout(), qs, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	c.Flags().BoolVar(&watch, "watch", false, "stream new questions as they arrive")
	c.MarkFlagsMutuallyExclusive("json", "watch")
	return c
}

func printQuestions(out io.Writer, qs []fleet.PendingQuestion, asJSON bool) error {
	if asJSON {
		// Always emit a JSON array (never null) for a stable shape.
		if qs == nil {
			qs = []fleet.PendingQuestion{}
		}
		return json.NewEncoder(out).Encode(qs)
	}
	for _, q := range qs {
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", q.Agent, q.ID, age(q.Asked), q.Text); err != nil {
			return err
		}
	}
	return nil
}

// age renders a compact relative age ("12s", "3m", "2h", "1d"); empty when the
// timestamp is unknown.
func age(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// watchQuestions prints pending questions, then polls for new ones every 500ms,
// printing each id once (mirrors the inbox/logs follow loop).
func watchQuestions(ctx context.Context, f *fleet.Fleet, out io.Writer) error {
	seen := map[string]bool{}
	for {
		qs, err := f.Questions(ctx)
		if err != nil {
			return err
		}
		for _, q := range qs {
			key := q.Agent + "/" + q.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", q.Agent, q.ID, age(q.Asked), q.Text); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

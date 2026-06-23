package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func logsCmd(f *fleet.Fleet) *cobra.Command {
	var follow, asJSON bool
	c := &cobra.Command{
		Use:   "logs <agent>",
		Short: "Stream an agent's container.log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := f.Logs(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
			}
			logPath := filepath.Join(info.LogDir, "container.log")
			if follow {
				return followLog(cmd.Context(), info.LogDir, logPath, cmd.OutOrStdout())
			}
			b, err := os.ReadFile(logPath)
			if err != nil {
				return fmt.Errorf("read log for %q: %w", args[0], err)
			}
			_, err = cmd.OutOrStdout().Write(b)
			return err
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "stream new log output until the agent finishes")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON metadata (logDir, status, transcript)")
	return c
}

// followLog tails container.log, draining new bytes every 200ms until the
// session status file reads "done" (then it drains once more and exits).
func followLog(ctx context.Context, dir, logPath string, out io.Writer) error {
	var offset int64
	for {
		offset = drainFrom(logPath, offset, out)
		if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil && strings.TrimSpace(string(b)) == "done" {
			drainFrom(logPath, offset, out) // final drain
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// drainFrom copies container.log bytes from offset to out, returning the new
// offset. Missing file is treated as "nothing yet" (offset unchanged).
func drainFrom(path string, offset int64, out io.Writer) int64 {
	file, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	n, _ := io.Copy(out, file)
	return offset + n
}

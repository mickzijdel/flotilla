package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mickzijdel/flotilla/internal/backend"
	"github.com/mickzijdel/flotilla/internal/cli"
	"github.com/mickzijdel/flotilla/internal/fleet"
)

func main() {
	f := &fleet.Fleet{
		Backend:        backend.NewDocker(),
		BaseImage:      "ubuntu:24.04",
		EgressFirewall: true,
	}
	root := cli.BuildRoot(f)
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

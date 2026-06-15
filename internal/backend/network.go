package backend

import (
	"context"
	"strings"
)

func (d *dockerBackend) NetworkCreate(ctx context.Context, name string, internal bool) error {
	args := []string{"network", "create"}
	if internal {
		args = append(args, "--internal")
	}
	args = append(args, name)
	_, err := run(ctx, args...)
	return err
}

func (d *dockerBackend) NetworkRemove(ctx context.Context, name string) error {
	_, err := run(ctx, "network", "rm", name)
	return err
}

func (d *dockerBackend) NetworkConnect(ctx context.Context, network, id string) error {
	_, err := run(ctx, "network", "connect", network, id)
	return err
}

func (d *dockerBackend) NetworkDisconnect(ctx context.Context, network, id string) error {
	_, err := run(ctx, "network", "disconnect", network, id)
	return err
}

// ContainerNetworks lists the networks a container is attached to.
func (d *dockerBackend) ContainerNetworks(ctx context.Context, id string) ([]string, error) {
	out, err := run(ctx, "inspect", "-f",
		`{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}`, id)
	if err != nil {
		return nil, err
	}
	var nets []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			nets = append(nets, line)
		}
	}
	return nets, nil
}

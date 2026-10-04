package cloud

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Server is a server as the Hetzner API reports it. kluster checks a new
// server here, never from Terraform output (docs/runbook/control-plane.md,
// step 1).
type Server struct {
	ID               string
	Name             string
	Status           string
	PublicIP         string
	PrivateIPs       []string
	DeleteProtected  bool
	RebuildProtected bool
}

// Server reads the named server, or returns nil if the project has none.
func (c *Client) Server(ctx context.Context, name string) (*Server, error) {
	s, _, err := c.h.Server.GetByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("reading server %s: %w", name, err)
	}
	if s == nil {
		return nil, nil
	}
	out := &Server{
		ID:               strconv.FormatInt(s.ID, 10),
		Name:             s.Name,
		Status:           string(s.Status),
		DeleteProtected:  s.Protection.Delete,
		RebuildProtected: s.Protection.Rebuild,
	}
	if s.PublicNet.IPv4.IP != nil {
		out.PublicIP = s.PublicNet.IPv4.IP.String()
	}
	for _, n := range s.PrivateNet {
		if n.IP != nil {
			out.PrivateIPs = append(out.PrivateIPs, n.IP.String())
		}
	}
	return out, nil
}

// Running reports whether the server is running.
func (s *Server) Running() bool { return s.Status == string(hcloud.ServerStatusRunning) }

package cloud

import (
	"context"
	"fmt"
)

// CheckServerType refuses a server type a new server cannot get in
// location: one Hetzner does not have, does not offer there, has deprecated
// there, or has out of stock there. Terraform would only fail at the create,
// after the Worker set holding the type is committed.
func (c *Client) CheckServerType(ctx context.Context, name, location string) error {
	st, _, err := c.h.ServerType.GetByName(ctx, name)
	if err != nil {
		return fmt.Errorf("reading server type %s: %w", name, err)
	}
	if st == nil {
		return fmt.Errorf("Hetzner has no server type %s", name)
	}
	for _, l := range st.Locations {
		if l.Location == nil || l.Location.Name != location {
			continue
		}
		switch {
		case l.IsDeprecated():
			return fmt.Errorf("server type %s is deprecated in %s", name, location)
		case !l.Available:
			return fmt.Errorf("server type %s is out of stock in %s", name, location)
		}
		return nil
	}
	return fmt.Errorf("server type %s is not offered in %s", name, location)
}

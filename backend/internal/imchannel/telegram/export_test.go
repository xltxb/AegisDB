package telegram

import "context"

// CallForTest exposes call to the external test package.
func (c *Channel) CallForTest(ctx context.Context, method string, params, out any) error {
	return c.call(ctx, c.client, method, params, out)
}

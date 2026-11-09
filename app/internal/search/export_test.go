package search

import (
	"context"
	"fmt"
)

// DropIndex deletes this client's index, so a test leaves nothing behind on the cluster.
func (c *Client) DropIndex(ctx context.Context) error {
	res, err := c.es.Indices.Delete([]string{c.index}, c.es.Indices.Delete.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("drop index: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() && res.StatusCode != 404 {
		return fmt.Errorf("drop index: status %d", res.StatusCode)
	}
	return nil
}

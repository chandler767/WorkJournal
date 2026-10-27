package main

import (
	"context"
	"errors"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workjournal/internal/config"
	"workjournal/internal/mcpserver"
)

// runMCP serves the read-only journal MCP server on t until the client
// disconnects or ctx ends. It opens the database read-only, so it runs
// safely beside serve, and writes nothing to stdout but the protocol.
func runMCP(ctx context.Context, cfgPath string, t mcp.Transport) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	j, err := mcpserver.Open(cfg)
	if err != nil {
		return err
	}
	defer j.Close()
	ss, err := j.Server().Connect(ctx, t, nil)
	if err != nil {
		return err
	}
	if err := ss.Wait(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, mcp.ErrConnectionClosed) && ctx.Err() == nil {
		return err
	}
	return nil
}

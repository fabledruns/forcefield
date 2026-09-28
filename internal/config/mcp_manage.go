package config

import (
	"fmt"

	"forcefield/internal/mcp"
)

// MCP server management on *Config.
//
// These are pure map operations over the existing mcp: block plus the
// existing MCP validation: no second configuration representation, no
// I/O, no process handling. Callers persist with Save. Validation errors
// name the offending field so slash-command failures stay actionable.

// AddMCPServer inserts a new server entry after validating the key, the
// entry, and the resulting block (including the enabled-server budget).
// It refuses duplicates instead of overwriting.
func (c *Config) AddMCPServer(name string, sc mcp.ServerConfig) error {
	if c == nil {
		return fmt.Errorf("no config to modify")
	}
	if err := mcp.ValidateServerKey(name); err != nil {
		return err
	}
	if c.MCP.Servers == nil {
		c.MCP.Servers = make(map[string]mcp.ServerConfig)
	}
	if _, exists := c.MCP.Servers[name]; exists {
		return fmt.Errorf("mcp server %q already exists (use /mcp remove first to replace it)", name)
	}
	c.MCP.Servers[name] = sc
	if err := c.MCP.Validate(); err != nil {
		delete(c.MCP.Servers, name)
		return err
	}
	return nil
}

// RemoveMCPServer deletes a server entry. Only that entry is touched;
// unrelated configuration is never modified.
func (c *Config) RemoveMCPServer(name string) error {
	if c == nil {
		return fmt.Errorf("no config to modify")
	}
	if _, exists := c.MCP.Servers[name]; !exists {
		return fmt.Errorf("mcp server %q not found", name)
	}
	delete(c.MCP.Servers, name)
	return nil
}

// SetMCPServerEnabled flips a server's enabled state without touching
// any other field. Disabling never deletes configuration.
func (c *Config) SetMCPServerEnabled(name string, enabled bool) error {
	if c == nil {
		return fmt.Errorf("no config to modify")
	}
	sc, exists := c.MCP.Servers[name]
	if !exists {
		return fmt.Errorf("mcp server %q not found", name)
	}
	old := sc
	sc.Enabled = &enabled
	c.MCP.Servers[name] = sc
	if err := c.MCP.Validate(); err != nil {
		c.MCP.Servers[name] = old
		return err
	}
	return nil
}

// GetMCPServer returns a copy of one server entry.
func (c *Config) GetMCPServer(name string) (mcp.ServerConfig, error) {
	if c == nil {
		return mcp.ServerConfig{}, fmt.Errorf("no config to read")
	}
	sc, exists := c.MCP.Servers[name]
	if !exists {
		return mcp.ServerConfig{}, fmt.Errorf("mcp server %q not found", name)
	}
	return sc, nil
}

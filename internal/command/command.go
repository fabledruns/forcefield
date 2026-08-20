// Package command implements Forcefield's TUI-independent slash commands.
package command

import "forcefield/internal/session"

// Context is the session-facing interface used by commands.
type Context interface {
	Println(format string, args ...any)
	Clear()
	Quit()
	Model() string
	Provider() string
	SetModel(name string) error
	SetProvider(name string) error
	OpenSessionPicker(sessions []session.Session)
	OpenProviderPicker()
	OpenModelPicker()
}

// Command is a slash command.
type Command interface {
	Name() string
	Aliases() []string
	Description() string
	Usage() string
	Execute(ctx Context, args []string) error
}

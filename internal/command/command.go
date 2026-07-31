// Package command implements Forcefield's slash-command system for the
// interactive chat (e.g. /help, /clear, /model, /exit).
//
// The package is deliberately independent of the TUI and the runtime: it
// knows how to parse a line, look up a command by name or alias, and run
// it against a small Context interface. Nothing in here imports Bubble
// Tea. That keeps every piece, parsing, lookup, suggestion, dispatch,
// unit-testable in isolation, and it means the TUI is the only thing
// that has to know how a Context is actually implemented.
//
// Adding a new command never requires touching this package or any
// existing command: define a type that satisfies Command and register an
// instance of it at startup.
package command

import "forcefield/internal/session"

// Context is the set of operations a Command is allowed to perform on
// the session. It exists so commands never need to know about Bubble
// Tea, the runtime, or any other concrete type — only this interface.
// The interactive TUI model is the production implementation; tests can
// supply a small fake instead.
type Context interface {
	Println(format string, args ...any)
	Clear()
	Quit()
	Model() 				 string
	Provider() 				 string
	SetModel(name string) 	 error
	SetProvider(name string) error
	OpenSessionPicker(sessions []session.Session)
}

// Command is a single slash command. Implementations are independent,
// self-contained units, adding one never means modifying another or the
// dispatch logic that runs them.
type Command interface {
	Name() string
	Aliases() []string
	Description() string
	Usage() string
	Execute(ctx Context, args []string) error
}
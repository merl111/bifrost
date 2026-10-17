package ui

import "context"

// NewRemoteFilesystem adapts bounded environment callbacks to the UI read
// boundary. Paths returned by list/read are remote names and are never opened
// by the host UI.
func NewRemoteFilesystem(list func(context.Context, string) ([]FileEntry, error), read func(context.Context, string) (string, error)) Filesystem {
	return callbackFilesystem{list: list, read: read}
}

// WithRemoteTerminal is an explicit-argv alias for WithTerminalCommand.
func WithRemoteTerminal(argv []string, dir string) Option { return WithTerminalCommand(argv, dir) }

package cli

import "github.com/k0nsta/agterm-remote/internal/cli/mocks"

// Compile-time proof that the generated mocks still satisfy the interfaces
// they were generated from. An interface change otherwise leaves a stale mock
// that compiles on its own and silently stops implementing anything — which is
// how MockOpenerRemote kept an AgrPath method after the interface moved to
// ResolveAgrPath. This lives in a test file on purpose: production code must
// not import the mocks package.
var (
	_ OpenerRemote = (*mocks.MockOpenerRemote)(nil)
	_ Sessions     = (*mocks.MockSessions)(nil)
)

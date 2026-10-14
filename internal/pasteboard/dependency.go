package pasteboard

import "context"

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// Runner executes one local command and returns its stdout. It is deliberately
// small: osascript and defaults both communicate their useful result on
// stdout, while their exit status is enough for the screenshot defaults
// fallbacks.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

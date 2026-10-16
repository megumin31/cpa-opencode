// Package fault contains the shared status-bearing error used by plugin boundaries.
package fault

// Error preserves an intentional client status and a safe, already prepared message.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string   { return e.Message }
func (e *Error) StatusCode() int { return e.Status }

// New marks an error originating in the plugin rather than an upstream response.
func New(status int, message string) error {
	return &Error{Status: status, Message: "OpenCode plugin: " + message}
}

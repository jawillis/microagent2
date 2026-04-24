package messaging

import "errors"

var (
	ErrInvalidMessage = errors.New("invalid message format")
	ErrTimeout        = errors.New("operation timed out")
	ErrNoSlot         = errors.New("no slot available")
	ErrPreempted      = errors.New("agent was preempted")
)

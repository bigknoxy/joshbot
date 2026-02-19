package bus

// MessageBus is a minimal stub used by channel tests.
type MessageBus struct{}

// NewMessageBus returns a new stub MessageBus.
func NewMessageBus() *MessageBus {
	return &MessageBus{}
}

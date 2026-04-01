package bus

import (
	"testing"
)

func TestNewMessageBus(t *testing.T) {
	mb := NewMessageBus()
	if mb == nil {
		t.Fatal("NewMessageBus() returned nil")
	}
}

package channels

import "github.com/bigknoxy/joshbot/pkg/bus"

// BaseChannel is a lightweight implementation used in tests.
type BaseChannel struct {
	name     string
	bus      *bus.MessageBus
	allowSet map[string]struct{}
}

// NewBaseChannel creates a BaseChannel. allowlist entries may include '@' and mixed case.
func NewBaseChannel(name string, mb *bus.MessageBus, allowlist []string) *BaseChannel {
	bc := &BaseChannel{name: name, bus: mb, allowSet: make(map[string]struct{})}
	for _, a := range allowlist {
		// normalize: strip leading '@' and lowercase
		s := a
		if len(s) > 0 && s[0] == '@' {
			s = s[1:]
		}
		// lowercase simple ASCII
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= 'A' && c <= 'Z' {
				b := []byte(s)
				b[i] = c + ('a' - 'A')
				s = string(b)
			}
		}
		bc.allowSet[s] = struct{}{}
	}
	return bc
}

// IsAllowed checks if a username is allowed (case-insensitive, optional leading '@').
func (b *BaseChannel) IsAllowed(name string) bool {
	if len(name) > 0 && name[0] == '@' {
		name = name[1:]
	}
	// lowercase
	s := name
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			bts := []byte(s)
			bts[i] = c + ('a' - 'A')
			s = string(bts)
		}
	}
	_, ok := b.allowSet[s]
	return ok
}

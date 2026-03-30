package overlays

// Stack manages the active overlay stack.
// The topmost (last) item receives input first.
type Stack struct {
	items []Overlay
}

// Push adds an overlay to the top of the stack.
func (s *Stack) Push(o Overlay) { s.items = append(s.items, o) }

// Pop removes and returns the topmost overlay. Returns nil if empty.
func (s *Stack) Pop() Overlay {
	if len(s.items) == 0 {
		return nil
	}
	top := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return top
}

// Top returns the topmost overlay without removing it. Returns nil if empty.
func (s *Stack) Top() Overlay {
	if len(s.items) == 0 {
		return nil
	}
	return s.items[len(s.items)-1]
}

// IsEmpty reports whether the stack has no overlays.
func (s *Stack) IsEmpty() bool { return len(s.items) == 0 }

// Len returns the number of overlays on the stack.
func (s *Stack) Len() int { return len(s.items) }

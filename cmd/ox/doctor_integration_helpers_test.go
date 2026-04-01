//go:build !short

package main

import (
	"fmt"
)

// mockAgent is a mock implementation of the Agent interface for testing
type mockAgent struct {
	detectProject bool
	detectCLI     bool
	hasHooks      bool
	hasUserHooks  bool
	installCalled bool
}

func (m *mockAgent) Name() string {
	return "MockAgent"
}

func (m *mockAgent) Install(user bool) error {
	m.installCalled = true
	m.hasHooks = !user
	m.hasUserHooks = user
	return nil
}

func (m *mockAgent) Uninstall(user bool) error {
	return nil
}

func (m *mockAgent) HasHooks(user bool) bool {
	if user {
		return m.hasUserHooks
	}
	return m.hasHooks
}

func (m *mockAgent) List() map[string]bool {
	return map[string]bool{}
}

func (m *mockAgent) Detect() bool {
	return m.detectProject || m.detectCLI
}

func (m *mockAgent) DetectProject() bool {
	return m.detectProject
}

func (m *mockAgent) DetectCLI() bool {
	return m.detectCLI
}

func (m *mockAgent) SupportsHooks() bool {
	return true
}

// mockAgentWithError simulates installation errors
type mockAgentWithError struct {
	detectProject bool
	detectCLI     bool
}

func (m *mockAgentWithError) Name() string {
	return "MockAgentWithError"
}

func (m *mockAgentWithError) Install(user bool) error {
	return fmt.Errorf("simulated installation error")
}

func (m *mockAgentWithError) Uninstall(user bool) error {
	return nil
}

func (m *mockAgentWithError) HasHooks(user bool) bool {
	return false
}

func (m *mockAgentWithError) List() map[string]bool {
	return map[string]bool{}
}

func (m *mockAgentWithError) Detect() bool {
	return m.detectProject || m.detectCLI
}

func (m *mockAgentWithError) DetectProject() bool {
	return m.detectProject
}

func (m *mockAgentWithError) DetectCLI() bool {
	return m.detectCLI
}

func (m *mockAgentWithError) SupportsHooks() bool {
	return true
}

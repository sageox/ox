package cli

import (
	"fmt"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// SpinnerResult holds the result of an async operation
type SpinnerResult[T any] struct {
	Value T
	Err   error
}

// spinnerModel is the bubbletea model for a generic spinner
type spinnerModel[T any] struct {
	spinner spinner.Model
	message string
	result  chan SpinnerResult[T]
	done    bool
	output  SpinnerResult[T]
}

func newSpinnerModel[T any](message string) spinnerModel[T] {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = SpinnerStyle
	return spinnerModel[T]{
		spinner: s,
		message: message,
		result:  make(chan SpinnerResult[T], 1),
	}
}

func (m spinnerModel[T]) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.waitForResult(),
	)
}

func (m spinnerModel[T]) waitForResult() tea.Cmd {
	return func() tea.Msg {
		return <-m.result
	}
}

func (m spinnerModel[T]) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.done = true
			m.output.Err = tea.ErrInterrupted
			return m, tea.Quit
		}
	case SpinnerResult[T]:
		m.done = true
		m.output = msg
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m spinnerModel[T]) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	return tea.NewView(fmt.Sprintf("%s %s", m.spinner.View(), m.message))
}

// WithSpinner runs an async function with a spinner displayed.
// Shows spinner after a short delay to avoid flicker for fast operations.
// In non-interactive mode (CI or --no-interactive), runs without spinner.
// Returns the result of the function, or tea.ErrInterrupted if dismissed before
// the function finishes. Interrupting stops the wait, not the function itself.
func WithSpinner[T any](message string, fn func() (T, error)) (T, error) {
	// skip spinner in non-interactive mode (CI, --no-interactive)
	if !IsInteractive() {
		return fn()
	}

	// for very fast operations, don't show spinner at all
	// use a channel to coordinate
	resultCh := make(chan SpinnerResult[T], 1)
	var once sync.Once

	// start the operation
	go func() {
		val, err := fn()
		once.Do(func() {
			resultCh <- SpinnerResult[T]{Value: val, Err: err}
		})
	}()

	// wait a short time before showing spinner
	select {
	case result := <-resultCh:
		// operation completed quickly, no spinner needed
		return result.Value, result.Err
	case <-time.After(300 * time.Millisecond):
		// operation is taking a while, show spinner
	}

	// create and run spinner
	model := newSpinnerModel[T](message)

	// forward result to spinner
	go func() {
		result := <-resultCh
		model.result <- result
	}()

	program := tea.NewProgram(model)
	finalModel, err := program.Run()
	if err != nil {
		var zero T
		return zero, fmt.Errorf("spinner error: %w", err)
	}

	final := finalModel.(spinnerModel[T])
	if !final.done {
		var zero T
		return zero, tea.ErrInterrupted
	}
	return final.output.Value, final.output.Err
}

// WithSpinnerNoResult runs an async function with a spinner displayed.
// Use when the function returns only an error.
func WithSpinnerNoResult(message string, fn func() error) error {
	_, err := WithSpinner(message, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

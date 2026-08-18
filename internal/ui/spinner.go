package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// spinnerFrames is the Braille cycle used by most modern CLIs.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner is a single-line progress indicator.
//
// It is the one place the CLI moves the cursor, and it only ever does so on an
// interactive terminal: everywhere else Start is a no-op, so redirected output
// never accumulates carriage returns or escape codes.
type Spinner struct {
	w       io.Writer
	p       Palette
	label   string
	enabled bool

	mu     sync.Mutex
	stop   chan struct{}
	done   chan struct{}
	active bool
}

// NewSpinner builds a spinner. When enabled is false every method is inert.
func NewSpinner(w io.Writer, enabled, color bool, label string) *Spinner {
	return &Spinner{w: w, p: newPalette(color), label: label, enabled: enabled}
}

// Start begins animating until Stop is called.
func (s *Spinner) Start() {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return
	}
	s.active = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})

	go func(stop <-chan struct{}, done chan<- struct{}) {
		defer close(done)
		start := time.Now()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-ticker.C:
				frame := spinnerFrames[i%len(spinnerFrames)]
				elapsed := time.Since(start).Truncate(time.Second)
				secs := int(elapsed.Seconds())
				// \r returns to column 0 and \033[K clears the rest of the
				// line, so a shorter frame never leaves debris behind.
				fmt.Fprintf(s.w, "\r\033[K%s%s%s %s%s %ds%s",
					s.p.Cyan, frame, s.p.Reset,
					s.p.Dim, s.label, secs, s.p.Reset)
			}
		}
	}(s.stop, s.done)
}

// Stop halts the animation and erases the line.
func (s *Spinner) Stop() {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	close(s.stop)
	done := s.done
	s.mu.Unlock()

	<-done
	fmt.Fprint(s.w, "\r\033[K")
}

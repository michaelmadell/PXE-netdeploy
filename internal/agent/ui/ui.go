// Package ui is the agent's console screen in WinPE (ADR 0013, 0025): plain
// text with VT sequences where the console supports them, keys read raw.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Console is the WinPE console.
type Console struct {
	out   io.Writer
	keys  chan byte
	vt    bool
	once  sync.Once
	mu    sync.Mutex
	title string
	lines []string // log tail shown under progress
}

// New returns the console on stdin/stdout, in raw mode.
func New(title string) *Console {
	c := &Console{out: os.Stdout, keys: make(chan byte, 64), title: title}
	c.vt = enableVT()
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		term.MakeRaw(fd)
	}
	go func() {
		b := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(b)
			if err != nil {
				return
			}
			if n == 1 {
				c.keys <- b[0]
			}
		}
	}()
	return c
}

func (c *Console) printf(format string, a ...any) {
	fmt.Fprintf(c.out, strings.ReplaceAll(format, "\n", "\r\n"), a...)
}

// Screen clears the screen and shows the title and a heading.
func (c *Console) Screen(heading string) {
	if c.vt {
		c.printf("\x1b[2J\x1b[H")
	} else {
		c.printf("\n\n")
	}
	c.printf("%s\n%s\n\n%s\n\n", c.title, strings.Repeat("=", len(c.title)), heading)
}

// Line prints a line.
func (c *Console) Line(format string, a ...any) { c.printf(format+"\n", a...) }

// Banner prints a warning line (inverse video where supported).
func (c *Console) Banner(text string) {
	if c.vt {
		c.printf("\x1b[7m %s \x1b[0m\n\n", text)
		return
	}
	c.printf("*** %s ***\n\n", text)
}

func (c *Console) drain() {
	for {
		select {
		case <-c.keys:
		default:
			return
		}
	}
}

// Countdown shows text with the seconds left until any key is pressed or
// the time runs out. It reports whether a key was pressed.
func (c *Console) Countdown(text string, d time.Duration) bool {
	c.drain()
	end := time.Now().Add(d)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		left := time.Until(end)
		if left <= 0 {
			c.printf("\r%s 0 s \n", text)
			return false
		}
		c.printf("\r%s %d s ", text, int(left.Round(time.Second)/time.Second))
		select {
		case <-c.keys:
			c.printf("\n")
			return true
		case <-tick.C:
		}
	}
}

// ReadLine reads a line. mask hides it (passwords). With timeout > 0 the
// countdown runs until the first key; ok=false when it ran out.
func (c *Console) ReadLine(prompt string, mask bool, timeout time.Duration) (string, bool) {
	c.drain()
	var buf []rune
	end := time.Now().Add(timeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	show := func() {
		shown := string(buf)
		if mask {
			shown = strings.Repeat("*", len(buf))
		}
		suffix := ""
		if timeout > 0 && len(buf) == 0 {
			suffix = fmt.Sprintf("  (%d s)", max(0, int(time.Until(end).Round(time.Second)/time.Second)))
		}
		c.printf("\r%s%s%s\x1b[K", prompt, shown, suffix)
	}
	show()
	started := false
	for {
		select {
		case k := <-c.keys:
			started = true
			switch k {
			case '\r', '\n':
				c.printf("\n")
				return string(buf), true
			case 8, 127: // backspace
				if len(buf) > 0 {
					buf = buf[:len(buf)-1]
				}
			case 3: // Ctrl+C
				c.printf("\n")
				return "", false
			default:
				if k >= 32 && k < 127 {
					buf = append(buf, rune(k))
				}
			}
			show()
		case <-tick.C:
			if timeout > 0 && !started {
				if time.Now().After(end) {
					c.printf("\n")
					return "", false
				}
				show()
			}
		}
	}
}

// Menu shows numbered items with def preselected and returns the chosen
// index; ok=false if cancelled (Esc).
func (c *Console) Menu(items []string, def int) (int, bool) {
	for i, it := range items {
		mark := "  "
		if i == def {
			mark = "> "
		}
		c.printf("%s%d. %s\n", mark, i+1, it)
	}
	c.printf("\n")
	for {
		s, ok := c.ReadLine(fmt.Sprintf("Choose 1-%d (Enter: %d): ", len(items), def+1), false, 0)
		if !ok {
			return 0, false
		}
		s = strings.TrimSpace(s)
		if s == "" && def >= 0 {
			return def, true
		}
		var n int
		if _, err := fmt.Sscan(s, &n); err == nil && n >= 1 && n <= len(items) {
			return n - 1, true
		}
		c.printf("Not a choice.\n")
	}
}

// Confirm asks a yes/no question.
func (c *Console) Confirm(question string) bool {
	s, ok := c.ReadLine(question+" Type yes to continue: ", false, 0)
	return ok && strings.EqualFold(strings.TrimSpace(s), "yes")
}

// WaitKey waits for a key or d.
func (c *Console) WaitKey(d time.Duration) {
	c.drain()
	select {
	case <-c.keys:
	case <-time.After(d):
	}
}

// Progress redraws the progress screen.
func (c *Console) Progress(heading, stage string, percent int, message string) {
	c.mu.Lock()
	lines := append([]string{}, c.lines...)
	c.mu.Unlock()
	c.Screen(heading)
	bar := strings.Repeat("#", percent/5) + strings.Repeat(".", 20-percent/5)
	c.printf("%-18s [%s] %3d%%\n%s\n\n", stage, bar, percent, message)
	for _, l := range lines {
		c.printf("%s\n", l)
	}
}

// Log adds a line to the log tail (the last 12 lines are shown).
func (c *Console) Log(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
	if len(c.lines) > 12 {
		c.lines = c.lines[len(c.lines)-12:]
	}
}

package gateway

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// What a terminal that waits for the VM is shown (spec 18, "Boot-aware UX"). Everything here
// is text made from a view of the wake: nothing reads a clock or a connection, so every
// transcript can be tested byte for byte.

// phaseState is where one line of the screen is.
type phaseState int

const (
	phasePending phaseState = iota
	phaseActive
	phaseDone
	phaseFailed
)

// bootPhase is one line: the VM coming up, or its SSH server answering.
type bootPhase struct {
	State   phaseState
	Elapsed time.Duration // while active: so far; when done: what it took
	ETA     time.Duration // while active: what is left, as far as past wakes tell
	Error   string        // when failed
}

// bootView is the wake as one moment of an SSH connection sees it.
type bootView struct {
	Kind   proto.WakeKind // restore or cold
	Joined bool           // the wake was already under way when the connection came
	ETA    time.Duration  // of the whole wait, when it began
	VM     bootPhase
	SSH    bootPhase
	Ready  bool
	Total  time.Duration // when ready: how long the connection waited
}

// terminal is what the client said about where the text goes.
type terminal struct {
	Colour  bool // TERM is not dumb
	Unicode bool // LANG or LC_* mention UTF-8
	Live    bool // lines can be redrawn in place (TERM is not dumb)
}

// terminalOf reads a client's TERM (from its pty request) and the locale variables it sent.
func terminalOf(term string, env map[string]string) terminal {
	smart := term != "" && term != "dumb"
	unicode := false
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		value := strings.ToUpper(env[name])
		if strings.Contains(value, "UTF-8") || strings.Contains(value, "UTF8") {
			unicode = true
		}
	}
	return terminal{Colour: smart, Unicode: unicode, Live: smart}
}

// The boot screen's success, active, failure and muted tokens, as the nearest of the 256
// colours every terminal has.
const (
	ansiSuccess = "\x1b[38;5;114m"
	ansiActive  = "\x1b[38;5;75m"
	ansiFailure = "\x1b[38;5;203m"
	ansiMuted   = "\x1b[38;5;245m"
	ansiReset   = "\x1b[0m"
	eraseToEnd  = "\x1b[K"
)

// seconds is a duration as the screen says it: 5.8s.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1fs", math.Round(d.Seconds()*10)/10)
}

// about is an estimate as the screen says it: about 7s, never less than one.
func about(d time.Duration) string {
	return fmt.Sprintf("about %ds", max(1, int(math.Ceil(d.Seconds()))))
}

// bootScreen draws a wake on an interactive terminal, a step at a time.
type bootScreen struct {
	term   terminal
	header bool   // the first line is out
	open   string // the phase whose line is drawn and not yet final: "", "vm" or "ssh"
	vm     bool   // the VM line is final
	ssh    bool   // the SSH line is final
	ended  bool
}

func (s *bootScreen) paint(colour, text string) string {
	if !s.term.Colour || colour == "" {
		return text
	}
	return colour + text + ansiReset
}

func (s *bootScreen) symbol(state phaseState) string {
	symbols := map[phaseState][2]string{phaseActive: {"●", "*"}, phaseDone: {"✓", "+"}, phaseFailed: {"✗", "-"}}[state]
	colour := map[phaseState]string{phaseActive: ansiActive, phaseDone: ansiSuccess, phaseFailed: ansiFailure}[state]
	if s.term.Unicode {
		return s.paint(colour, symbols[0])
	}
	return s.paint(colour, symbols[1])
}

// line is one phase as a line, without its end.
func (s *bootScreen) line(label string, phase bootPhase, done string) string {
	text := ""
	switch phase.State {
	case phaseActive:
		separator := " · "
		if !s.term.Unicode {
			separator = " - "
		}
		text = seconds(phase.Elapsed) + s.paint(ansiMuted, separator+about(phase.ETA))
	case phaseDone:
		text = done
	case phaseFailed:
		text = phase.Error
	case phasePending:
	}
	return fmt.Sprintf("  %s %-12s %s", s.symbol(phase.State), label, text)
}

// draw returns what to write so that the terminal shows the view, given what it shows already.
func (s *bootScreen) draw(view bootView) string {
	if s.ended {
		return ""
	}
	var out strings.Builder
	if !s.header {
		s.header = true
		dash := "—"
		if !s.term.Unicode {
			dash = "-"
		}
		what := "devbox is asleep " + dash + " waking it up"
		switch {
		case view.Joined:
			what = "devbox is waking up"
		case view.Kind == proto.WakeKindCold:
			what = "devbox is off " + dash + " starting it"
		}
		out.WriteString(what + " (" + about(view.ETA) + ")\r\n\r\n")
	}
	// A line that is still changing is drawn again where it is; one that is final gets its end.
	phase := func(name, label string, phase bootPhase, done string, final *bool) {
		if *final || phase.State == phasePending {
			return
		}
		settled := phase.State == phaseDone || phase.State == phaseFailed
		if !settled && !s.term.Live {
			return // a terminal that cannot redraw is told once, when the phase is over
		}
		if s.open == name {
			out.WriteString("\r")
		}
		out.WriteString(s.line(label, phase, done))
		if s.term.Live {
			out.WriteString(eraseToEnd)
		}
		s.open = name
		if settled {
			out.WriteString("\r\n")
			s.open, *final = "", true
		}
	}
	vmDone := "restored in " + seconds(view.VM.Elapsed)
	switch {
	case view.Joined && view.VM.Elapsed == 0:
		vmDone = "running"
	case view.Kind == proto.WakeKindCold:
		vmDone = "started in " + seconds(view.VM.Elapsed)
	}
	phase("vm", "VM", view.VM, vmDone, &s.vm)
	phase("ssh", "SSH", view.SSH, "answers after "+seconds(view.SSH.Elapsed), &s.ssh)

	if view.VM.State == phaseFailed || view.SSH.State == phaseFailed {
		s.ended = true
	}
	if view.Ready {
		s.ended = true
		out.WriteString("    " + fmt.Sprintf("%-12s ", "Repos") + s.paint(ansiMuted, "sync continues in the background") + "\r\n\r\n")
		how := "restored"
		if view.Kind == proto.WakeKindCold {
			how = "booted"
		}
		out.WriteString("  Ready in " + seconds(view.Total) + " (" + how + ").\r\n\r\n")
	}
	return out.String()
}

// failure is what follows a failed line: where to look, and nothing more.
func (s *bootScreen) failure(codeHost string) string {
	return "\r\nSee https://" + codeHost + " for details.\r\n"
}

// The lines a connection without a terminal gets on its standard error.

func plainWaking(eta time.Duration) string {
	return "devbox: waking the VM (" + about(eta) + ")…\n"
}

func plainReady(total time.Duration) string { return "devbox: ready in " + seconds(total) + "\n" }

func plainFailed(reason string) string { return "devbox: wake failed: " + reason + "\n" }

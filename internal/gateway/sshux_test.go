package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// Every transcript is what a terminal receives, byte for byte: the sequence of views a wake
// goes through, drawn one after the other.

const (
	ok     = "\x1b[38;5;114m✓\x1b[0m"
	dot    = "\x1b[38;5;75m●\x1b[0m"
	cross  = "\x1b[38;5;203m✗\x1b[0m"
	el     = "\x1b[K"
	muted  = "\x1b[38;5;245m"
	normal = "\x1b[0m"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func transcript(term terminal, views ...bootView) string {
	screen := &bootScreen{term: term}
	var out strings.Builder
	for _, view := range views {
		out.WriteString(screen.draw(view))
	}
	return out.String()
}

var xterm = terminalOf("xterm-256color", map[string]string{"LANG": "en_US.UTF-8"})

func active(elapsed, eta int) bootPhase {
	return bootPhase{State: phaseActive, Elapsed: ms(elapsed), ETA: ms(eta)}
}

func done(took int) bootPhase { return bootPhase{State: phaseDone, Elapsed: ms(took)} }

func check(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("transcript:\n%q\nwant:\n%q", got, want)
	}
}

func TestTheTranscriptOfARestore(t *testing.T) {
	restore := proto.WakeKindRestore
	got := transcript(xterm,
		bootView{Kind: restore, ETA: ms(6800), VM: active(0, 5800)},
		bootView{Kind: restore, ETA: ms(6800), VM: active(2100, 3700)},
		bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: active(0, 1000)},
		bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: active(400, 600)},
		bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: done(600), Ready: true, Total: ms(6400)},
		bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: done(600), Ready: true, Total: ms(6400)}, // drawn once
	)
	check(t, got, "devbox is asleep — waking it up (about 7s)\r\n\r\n"+
		"  "+dot+" VM           0.0s"+muted+" · about 6s"+normal+el+
		"\r  "+dot+" VM           2.1s"+muted+" · about 4s"+normal+el+
		"\r  "+ok+" VM           restored in 5.8s"+el+"\r\n"+
		"  "+dot+" SSH          0.0s"+muted+" · about 1s"+normal+el+
		"\r  "+dot+" SSH          0.4s"+muted+" · about 1s"+normal+el+
		"\r  "+ok+" SSH          answers after 0.6s"+el+"\r\n"+
		"    Repos        "+muted+"sync continues in the background"+normal+"\r\n\r\n"+
		"  Ready in 6.4s (restored).\r\n\r\n")
}

func TestTheTranscriptOfAColdBoot(t *testing.T) {
	cold := proto.WakeKindCold
	got := transcript(xterm,
		bootView{Kind: cold, ETA: ms(22000), VM: active(100, 20900)},
		bootView{Kind: cold, ETA: ms(22000), VM: done(18200), SSH: active(3000, 0)},
		bootView{Kind: cold, ETA: ms(22000), VM: done(18200), SSH: done(3500), Ready: true, Total: ms(21700)},
	)
	check(t, got, "devbox is off — starting it (about 22s)\r\n\r\n"+
		"  "+dot+" VM           0.1s"+muted+" · about 21s"+normal+el+
		"\r  "+ok+" VM           started in 18.2s"+el+"\r\n"+
		"  "+dot+" SSH          3.0s"+muted+" · about 1s"+normal+el+
		"\r  "+ok+" SSH          answers after 3.5s"+el+"\r\n"+
		"    Repos        "+muted+"sync continues in the background"+normal+"\r\n\r\n"+
		"  Ready in 21.7s (booted).\r\n\r\n")
}

// A wake that was already running when the connection came is shown from where it is.
func TestTheTranscriptOfAWakeThatWasUnderWay(t *testing.T) {
	restore := proto.WakeKindRestore
	got := transcript(xterm,
		bootView{Kind: restore, Joined: true, ETA: ms(1000), VM: done(0), SSH: active(200, 800)},
		bootView{Kind: restore, Joined: true, ETA: ms(1000), VM: done(0), SSH: done(700), Ready: true, Total: ms(700)},
	)
	check(t, got, "devbox is waking up (about 1s)\r\n\r\n"+
		"  "+ok+" VM           running"+el+"\r\n"+
		"  "+dot+" SSH          0.2s"+muted+" · about 1s"+normal+el+
		"\r  "+ok+" SSH          answers after 0.7s"+el+"\r\n"+
		"    Repos        "+muted+"sync continues in the background"+normal+"\r\n\r\n"+
		"  Ready in 0.7s (restored).\r\n\r\n")
}

func TestTheTranscriptOfAFailureAndOfATimeout(t *testing.T) {
	restore := proto.WakeKindRestore
	screen := &bootScreen{term: xterm}
	failed := screen.draw(bootView{Kind: restore, ETA: ms(6800), VM: active(900, 4900)}) +
		screen.draw(bootView{Kind: restore, ETA: ms(6800), VM: bootPhase{State: phaseFailed, Error: `incus: Failed to start device "eth0"`}}) +
		screen.failure("code.example.com") +
		screen.draw(bootView{Kind: restore, VM: active(2000, 0)}) // nothing is drawn after the end
	check(t, failed, "devbox is asleep — waking it up (about 7s)\r\n\r\n"+
		"  "+dot+" VM           0.9s"+muted+" · about 5s"+normal+el+
		"\r  "+cross+" VM           incus: Failed to start device \"eth0\""+el+"\r\n"+
		"\r\nSee https://code.example.com for details.\r\n")

	screen = &bootScreen{term: xterm}
	timedOut := screen.draw(bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: active(170000, 0)}) +
		screen.draw(bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: bootPhase{State: phaseFailed, Error: "the VM's SSH server did not answer in 3 minutes"}}) +
		screen.failure("code.example.com")
	check(t, timedOut, "devbox is asleep — waking it up (about 7s)\r\n\r\n"+
		"  "+ok+" VM           restored in 5.8s"+el+"\r\n"+
		"  "+dot+" SSH          170.0s"+muted+" · about 1s"+normal+el+
		"\r  "+cross+" SSH          the VM's SSH server did not answer in 3 minutes"+el+"\r\n"+
		"\r\nSee https://code.example.com for details.\r\n")
	if waited(180*time.Second) != "3 minutes" || waited(time.Minute) != "a minute" || waited(300*time.Millisecond) != "300ms" {
		t.Errorf("timeouts in words: %q, %q, %q", waited(180*time.Second), waited(time.Minute), waited(300*time.Millisecond))
	}
}

// A dumb terminal cannot redraw a line or show colour: each phase is one line, when it is over.
func TestTheTranscriptOnADumbTerminal(t *testing.T) {
	restore := proto.WakeKindRestore
	dumb := terminalOf("dumb", map[string]string{"LC_ALL": "en_US.UTF-8"})
	got := transcript(dumb,
		bootView{Kind: restore, ETA: ms(6800), VM: active(0, 5800)},
		bootView{Kind: restore, ETA: ms(6800), VM: active(2100, 3700)},
		bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: active(400, 600)},
		bootView{Kind: restore, ETA: ms(6800), VM: done(5800), SSH: done(600), Ready: true, Total: ms(6400)},
	)
	check(t, got, "devbox is asleep — waking it up (about 7s)\r\n\r\n"+
		"  ✓ VM           restored in 5.8s\r\n"+
		"  ✓ SSH          answers after 0.6s\r\n"+
		"    Repos        sync continues in the background\r\n\r\n"+
		"  Ready in 6.4s (restored).\r\n\r\n")
	if strings.Contains(got, "\x1b") {
		t.Error("an escape sequence on a dumb terminal")
	}
}

// Without a UTF-8 locale the symbols are plain characters.
func TestTheTranscriptWithoutUTF8(t *testing.T) {
	restore := proto.WakeKindRestore
	ascii := terminalOf("vt100", map[string]string{"LANG": "C"})
	got := transcript(ascii,
		bootView{Kind: restore, ETA: ms(6800), VM: active(2100, 3700)},
		bootView{Kind: restore, ETA: ms(6800), VM: bootPhase{State: phaseFailed, Error: "no room"}},
	)
	check(t, got, "devbox is asleep - waking it up (about 7s)\r\n\r\n"+
		"  \x1b[38;5;75m*\x1b[0m VM           2.1s"+muted+" - about 4s"+normal+el+
		"\r  \x1b[38;5;203m-\x1b[0m VM           no room"+el+"\r\n")
	for _, r := range got {
		if r > 127 {
			t.Errorf("a character outside ASCII: %q", r)
		}
	}
	plus := transcript(ascii, bootView{Kind: restore, ETA: ms(1000), VM: done(1000), SSH: done(100), Ready: true, Total: ms(1100)})
	if !strings.Contains(plus, "\x1b[38;5;114m+\x1b[0m VM           restored in 1.0s") {
		t.Errorf("a finished phase without UTF-8: %q", plus)
	}
}

func TestTheLinesWithoutATerminal(t *testing.T) {
	check(t, plainWaking(ms(6800)), "devbox: waking the VM (about 7s)…\n")
	check(t, plainWaking(0), "devbox: waking the VM (about 1s)…\n")
	check(t, plainReady(ms(6449)), "devbox: ready in 6.4s\n")
	check(t, plainFailed(`incus: Failed to start device "eth0"`), "devbox: wake failed: incus: Failed to start device \"eth0\"\n")
}

func TestWhatATerminalCanShowIsReadFromWhatTheClientSent(t *testing.T) {
	for _, c := range []struct {
		term string
		env  map[string]string
		want terminal
	}{
		{"xterm-256color", map[string]string{"LANG": "en_US.UTF-8"}, terminal{Colour: true, Unicode: true, Live: true}},
		{"xterm", map[string]string{"LC_CTYPE": "de_DE.utf8"}, terminal{Colour: true, Unicode: true, Live: true}},
		{"xterm", nil, terminal{Colour: true, Live: true}},
		{"dumb", map[string]string{"LANG": "en_US.UTF-8"}, terminal{Unicode: true}},
		{"", nil, terminal{}},
	} {
		if got := terminalOf(c.term, c.env); got != c.want {
			t.Errorf("terminalOf(%q, %v) = %+v, want %+v", c.term, c.env, got, c.want)
		}
	}
}

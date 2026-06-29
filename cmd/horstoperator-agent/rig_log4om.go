package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// log4omBackend drives the rig via Log4OM 2's "Remote Control Interface" (UDP),
// per RemoteControlInterface v1.1. It sends XML <RemoteControlRequest> datagrams
// to Log4OM's Inbound UDP endpoint; Log4OM then performs the CAT write to the
// connected radio (via OmniRig).
//
// Readback: Log4OM answers on a *different* port — the inbound port + 1 — by
// broadcasting a <RemoteControlResponse> (or unsolicited <RadioInfo>). We bind
// that reply port (mirroring the PSTrotator client) and match the answer by
// MessageId to CONFIRM each command. A missing reply is treated as best-effort
// success (Log4OM may not be echoing / the broadcast may be blocked), but an
// explicit <Done>False</Done> is surfaced as a real error.
//
// There is no combined "tune" command, so a QSY sends SetTxFrequency then
// SetMode. Single VFO only — no preview (VFO-B) / split; those need a
// rigctld/FLRig backend. Satisfies rigController so /v1/rig/tune and /v1/operate
// work unchanged and the browser's "tune" capability flips on via Capabilities().
type log4omBackend struct {
	addr      string // inbound host:port (where Log4OM listens)
	replyPort int    // inbound port + 1 (where Log4OM answers), 0 = no readback
	timeout   time.Duration
	mu        sync.Mutex // serialises reply-port binds across Tune/ping
}

// log4omDefaultAddr is the loopback fallback. The actual port is whatever the
// operator sets in Log4OM (Connections → Inbound UDP); 2236 is the common
// default. The v1.1 spec defines no fixed port, so this is fully configurable.
const log4omDefaultAddr = "127.0.0.1:2236"

// log4omCmdSetFreq is the frequency message name. NOTE: the v1.1 spec's XML spells
// it "SetTxFrequeny" (sic — the missing 'c' is in the document, and its example
// response echoes the same spelling with <Done>True</Done>, so Log4OM accepts it
// verbatim). The parameter element <Frequency> is spelled correctly.
const log4omCmdSetFreq = "SetTxFrequeny"

type log4omCmd struct {
	id      string
	payload string
}

func newLog4OMBackend(addr string) *log4omBackend {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = log4omDefaultAddr
	}
	replyPort := 0
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		if p, perr := strconv.Atoi(portStr); perr == nil && p > 0 && p < 65535 {
			replyPort = p + 1
		}
	}
	return &log4omBackend{addr: addr, replyPort: replyPort, timeout: 1200 * time.Millisecond}
}

func (b *log4omBackend) Capabilities() rigCapabilities {
	return rigCapabilities{Tune: true} // preview/split intentionally false
}

func (b *log4omBackend) Tune(ctx context.Context, freqHz int64, mode string) error {
	if freqHz <= 0 {
		return fmt.Errorf("log4om: invalid frequency")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	freqID := newGUID()
	cmds := []log4omCmd{{
		id:      freqID,
		payload: b.build(freqID, log4omCmdSetFreq, fmt.Sprintf("<Frequency>%d</Frequency>", freqHz)),
	}}
	if m := log4omMode(mode); m != "" {
		modeID := newGUID()
		cmds = append(cmds, log4omCmd{id: modeID, payload: b.build(modeID, "SetMode", "<Mode>"+m+"</Mode>")})
	}
	return b.sendAndConfirm(ctx, cmds)
}

// ping sends an Alive request and waits for Log4OM's reply on the readback port.
// nil means Log4OM answered (it is up and the reply path works); an error means
// no readback (down, not echoing, reply port unavailable, or blocked). Used by
// the diagnostics rig probe.
func (b *log4omBackend) ping(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	listener := b.listenReply()
	if listener == nil {
		return fmt.Errorf("reply port %d unavailable", b.replyPort)
	}
	defer listener.Close()
	b.applyDeadline(ctx, listener)

	id := newGUID()
	if err := b.sendOnly(ctx, b.build(id, "Alive", "")); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	for {
		n, _, err := listener.ReadFromUDP(buf)
		if err != nil {
			return fmt.Errorf("no reply within %s", b.timeout)
		}
		if strings.Contains(string(buf[:n]), id) {
			return nil
		}
		// Not our answer (likely an unsolicited RadioInfo broadcast); keep reading.
	}
}

// sendAndConfirm sends every command up front, then reads the reply port for the
// matching <RemoteControlResponse>s until all are confirmed or the deadline hits.
// One read window covers the whole QSY, so the worst-case wait (no readback) is a
// single timeout, not one per command. <Done>False</Done> is a hard error.
func (b *log4omBackend) sendAndConfirm(ctx context.Context, cmds []log4omCmd) error {
	listener := b.listenReply()
	if listener != nil {
		defer listener.Close()
		b.applyDeadline(ctx, listener)
	}
	for _, cmd := range cmds {
		if err := b.sendOnly(ctx, cmd.payload); err != nil {
			return err
		}
	}
	if listener == nil {
		return nil // no reply port: best-effort, send-only
	}

	pending := make(map[string]bool, len(cmds))
	for _, cmd := range cmds {
		pending[cmd.id] = true
	}
	buf := make([]byte, 4096)
	for len(pending) > 0 {
		n, _, err := listener.ReadFromUDP(buf)
		if err != nil {
			return nil // timeout: unconfirmed remainder assumed sent (best-effort)
		}
		reply := strings.TrimSpace(string(buf[:n]))
		for id := range pending {
			if strings.Contains(reply, id) {
				delete(pending, id)
				if !log4omResponseOK(reply) {
					return fmt.Errorf("log4om: command not confirmed by Log4OM (%s)", log4omSummary(reply))
				}
				break
			}
		}
	}
	return nil
}

func (b *log4omBackend) build(id, name, body string) string {
	return fmt.Sprintf("<RemoteControlRequest><MessageId>%s</MessageId><RemoteControlMessage>%s</RemoteControlMessage>%s</RemoteControlRequest>", id, name, body)
}

// listenReply binds the readback port. Mirrors the PSTrotator client: bind IPv4
// explicitly ("udp4" + IPv4zero) because Log4OM answers as an IPv4 broadcast,
// which a dual-stack [::] socket would not receive. nil = readback unavailable.
func (b *log4omBackend) listenReply() *net.UDPConn {
	if b.replyPort <= 0 {
		return nil
	}
	l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: b.replyPort})
	if err != nil {
		return nil
	}
	return l
}

func (b *log4omBackend) applyDeadline(ctx context.Context, l *net.UDPConn) {
	deadline := time.Now().Add(b.timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = l.SetReadDeadline(deadline)
}

func (b *log4omBackend) sendOnly(ctx context.Context, payload string) error {
	ra, err := net.ResolveUDPAddr("udp", b.addr)
	if err != nil {
		return fmt.Errorf("log4om: resolve %s: %w", b.addr, err)
	}
	conn, err := net.DialUDP("udp", nil, ra)
	if err != nil {
		return fmt.Errorf("log4om: dial %s: %w", b.addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
	} else {
		_ = conn.SetWriteDeadline(time.Now().Add(b.timeout))
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("log4om: send to %s: %w", b.addr, err)
	}
	return nil
}

func log4omResponseOK(reply string) bool {
	return strings.Contains(strings.ToLower(reply), "<done>true</done>")
}

func log4omSummary(reply string) string {
	s := strings.Join(strings.Fields(reply), " ")
	if len(s) > 160 {
		s = s[:160] + "\u2026"
	}
	return s
}

// log4omMode maps the agent's already-normalized rig mode (cw/usb/lsb/am/fm/
// rtty/data, from rigModeForBackend) to the upper-case mode string Log4OM/OmniRig
// expect. Data modes collapse to "DATA"; an empty input skips the SetMode leg so
// a bare frequency QSY still works. Per-rig data-mode names (DIGU, PKTUSB, …) can
// be refined here if a particular radio needs it.
func log4omMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return ""
	case "cw":
		return "CW"
	case "usb":
		return "USB"
	case "lsb":
		return "LSB"
	case "am":
		return "AM"
	case "fm":
		return "FM"
	case "rtty":
		return "RTTY"
	case "data", "digi", "digital", "ft8", "ft4":
		return "DATA"
	default:
		return strings.ToUpper(strings.TrimSpace(mode))
	}
}

// newGUID returns a random RFC-4122 v4 UUID for the request <MessageId>.
func newGUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

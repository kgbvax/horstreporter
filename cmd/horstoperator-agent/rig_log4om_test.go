package main

import (
	"context"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var msgIDRe = regexp.MustCompile(`<MessageId>([^<]+)</MessageId>`)

// log4omResponder is a fake Log4OM: it listens on a UDP port (the "inbound" port)
// and, for each request, optionally echoes a <RemoteControlResponse> with the
// given <Done> value to the readback port (inbound+1), exactly as Log4OM does.
type log4omResponder struct {
	addr   string
	pc     *net.UDPConn
	mu     sync.Mutex
	got    []string
	replyP int
}

func startLog4OMResponder(t *testing.T, done string, reply bool) *log4omResponder {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("responder listen: %v", err)
	}
	la := pc.LocalAddr().(*net.UDPAddr)
	r := &log4omResponder{addr: la.String(), pc: pc, replyP: la.Port + 1}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := pc.ReadFromUDP(buf)
			if err != nil {
				return // closed by stop()
			}
			msg := string(buf[:n])
			r.mu.Lock()
			r.got = append(r.got, msg)
			r.mu.Unlock()
			if !reply {
				continue
			}
			id := ""
			if m := msgIDRe.FindStringSubmatch(msg); m != nil {
				id = m[1]
			}
			resp := "<RemoteControlResponse><MessageId>" + id + "</MessageId><Done>" + done + "</Done></RemoteControlResponse>"
			rc, e := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.replyP})
			if e == nil {
				_, _ = rc.Write([]byte(resp))
				rc.Close()
			}
		}
	}()
	return r
}

func (r *log4omResponder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func (r *log4omResponder) stop() { r.pc.Close() }

func TestLog4OMTuneSendsFreqAndMode(t *testing.T) {
	r := startLog4OMResponder(t, "", false) // silent: exercises the send path
	defer r.stop()

	b := newLog4OMBackend(r.addr)
	b.timeout = 150 * time.Millisecond // no readback will arrive; keep it quick
	if !b.Capabilities().Tune || b.Capabilities().Preview || b.Capabilities().Split {
		t.Fatalf("unexpected caps: %+v", b.Capabilities())
	}
	if b.replyPort != r.replyP {
		t.Fatalf("replyPort = %d, want %d (inbound+1)", b.replyPort, r.replyP)
	}

	if err := b.Tune(context.Background(), 14075000, "data"); err != nil {
		t.Fatalf("Tune should soft-succeed without readback: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(r.messages()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := r.messages()
	if len(got) < 2 {
		t.Fatalf("expected 2 datagrams, got %d", len(got))
	}
	freq, mode := got[0], got[1]
	if !strings.Contains(freq, "<RemoteControlMessage>SetTxFrequeny</RemoteControlMessage>") {
		t.Errorf("freq message missing SetTxFrequeny: %s", freq)
	}
	if !strings.Contains(freq, "<Frequency>14075000</Frequency>") {
		t.Errorf("freq message wrong frequency: %s", freq)
	}
	if !strings.Contains(freq, "<MessageId>") {
		t.Errorf("freq message missing MessageId: %s", freq)
	}
	if !strings.Contains(mode, "<RemoteControlMessage>SetMode</RemoteControlMessage>") {
		t.Errorf("mode message missing SetMode: %s", mode)
	}
	if !strings.Contains(mode, "<Mode>DATA</Mode>") {
		t.Errorf("mode message wrong mode: %s", mode)
	}
}

func TestLog4OMTuneReadbackConfirms(t *testing.T) {
	r := startLog4OMResponder(t, "True", true)
	defer r.stop()

	b := newLog4OMBackend(r.addr)
	b.timeout = 1 * time.Second
	if err := b.Tune(context.Background(), 14074000, "cw"); err != nil {
		t.Fatalf("Tune with Done=True readback should succeed: %v", err)
	}
}

func TestLog4OMTuneReadbackRejects(t *testing.T) {
	r := startLog4OMResponder(t, "False", true)
	defer r.stop()

	b := newLog4OMBackend(r.addr)
	b.timeout = 1 * time.Second
	if err := b.Tune(context.Background(), 14074000, "cw"); err == nil {
		t.Fatalf("Tune with Done=False readback should error")
	}
}

func TestLog4OMPing(t *testing.T) {
	r := startLog4OMResponder(t, "True", true)
	defer r.stop()

	b := newLog4OMBackend(r.addr)
	b.timeout = 1 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.ping(ctx); err != nil {
		t.Fatalf("ping should succeed against a responding Log4OM: %v", err)
	}

	// A silent endpoint (no Log4OM) must report no readback.
	silent := startLog4OMResponder(t, "", false)
	defer silent.stop()
	sb := newLog4OMBackend(silent.addr)
	sb.timeout = 200 * time.Millisecond
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()
	if err := sb.ping(ctx2); err == nil {
		t.Fatalf("ping should fail when no reply comes back")
	}
}

func TestLog4OMModeMapping(t *testing.T) {
	cases := map[string]string{
		"cw": "CW", "usb": "USB", "lsb": "LSB", "ft8": "DATA",
		"data": "DATA", "rtty": "RTTY", "": "", "weird": "WEIRD",
	}
	for in, want := range cases {
		if got := log4omMode(in); got != want {
			t.Errorf("log4omMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewGUIDFormat(t *testing.T) {
	g := newGUID()
	if len(g) != 36 || strings.Count(g, "-") != 4 {
		t.Fatalf("bad GUID shape: %q", g)
	}
	if g[14] != '4' { // version nibble
		t.Errorf("GUID not v4: %q", g)
	}
}

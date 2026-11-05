package tftp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// startServer serves dir on a loopback port and returns its address.
func startServer(t *testing.T, dir string, cfg Config) string {
	t.Helper()
	cfg.Root = dir
	if cfg.Timeout == 0 {
		cfg.Timeout = 200 * time.Millisecond
	}
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, conn) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
		s.Close()
	})
	return conn.LocalAddr().String()
}

func writeFile(t *testing.T, dir, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

// client is a minimal RFC 7440-aware TFTP reader used only by tests.
type client struct {
	opts map[string]string
	// skipACK, if set, is called before each ACK; returning true drops it.
	skipACK func(block int64) bool
	// abortAfterOACK sends ERROR 0 after the OACK, as tsize probes do.
	abortAfterOACK bool
	readTimeout    time.Duration // default 5s
}

type result struct {
	data []byte
	oack map[string]string
}

type tftpError struct {
	code uint16
	msg  string
}

func (e *tftpError) Error() string { return fmt.Sprintf("tftp error %d: %s", e.code, e.msg) }

func (c *client) get(addr, file string) (result, error) {
	var res result
	server, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return res, err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return res, err
	}
	defer conn.Close()

	rrq := []byte{0, byte(opRRQ)}
	rrq = append(append(rrq, file...), 0)
	rrq = append(append(rrq, "octet"...), 0)
	// Fixed order keeps tests deterministic.
	for _, k := range []string{"blksize", "tsize", "timeout", "windowsize"} {
		if v, ok := c.opts[k]; ok {
			rrq = append(append(rrq, k...), 0)
			rrq = append(append(rrq, v...), 0)
		}
	}
	if _, err := conn.WriteToUDP(rrq, server); err != nil {
		return res, err
	}

	blksize, window := 512, 1
	var peer *net.UDPAddr
	expect := int64(1) // next absolute block wanted
	inWindow := 0
	buf := make([]byte, 70000)
	var out bytes.Buffer

	ack := func(block int64) error {
		if c.skipACK != nil && c.skipACK(block) {
			return nil
		}
		p := make([]byte, 4)
		binary.BigEndian.PutUint16(p, opACK)
		binary.BigEndian.PutUint16(p[2:], uint16(block))
		_, err := conn.WriteToUDP(p, peer)
		return err
	}

	for {
		rt := c.readTimeout
		if rt == 0 {
			rt = 5 * time.Second
		}
		conn.SetReadDeadline(time.Now().Add(rt))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return res, err
		}
		if peer == nil {
			peer = from
		} else if !from.IP.Equal(peer.IP) || from.Port != peer.Port {
			continue
		}
		op := binary.BigEndian.Uint16(buf)
		switch op {
		case opERROR:
			code, msg := parseError(buf[:n])
			return res, &tftpError{code, msg}
		case opOACK:
			res.oack = map[string]string{}
			f := bytes.Split(buf[2:n], []byte{0})
			for i := 0; i+1 < len(f); i += 2 {
				res.oack[string(f[i])] = string(f[i+1])
			}
			if v, ok := res.oack["blksize"]; ok {
				blksize, _ = strconv.Atoi(v)
			}
			if v, ok := res.oack["windowsize"]; ok {
				window, _ = strconv.Atoi(v)
			}
			if c.abortAfterOACK {
				_, err := conn.WriteToUDP(errorPacket(0, "tsize probe"), peer)
				return res, err
			}
			if err := ack(0); err != nil {
				return res, err
			}
		case opDATA:
			block := binary.BigEndian.Uint16(buf[2:])
			if block != uint16(expect) {
				// Duplicate or out of order: re-ACK the last good block.
				if err := ack(expect - 1); err != nil {
					return res, err
				}
				inWindow = 0
				continue
			}
			payload := buf[4:n]
			out.Write(payload)
			last := len(payload) < blksize
			inWindow++
			if last || inWindow == window {
				if err := ack(expect); err != nil {
					return res, err
				}
				inWindow = 0
			}
			expect++
			if last {
				res.data = out.Bytes()
				return res, nil
			}
		}
	}
}

func TestReadPlain(t *testing.T) {
	dir := t.TempDir()
	want := randomBytes(1300)
	writeFile(t, dir, "bootmgfw.efi", want)
	addr := startServer(t, dir, Config{})

	res, err := (&client{}).get(addr, "bootmgfw.efi")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.data, want) {
		t.Fatalf("got %d bytes, want %d", len(res.data), len(want))
	}
	if res.oack != nil {
		t.Errorf("unexpected OACK %v", res.oack)
	}
}

func TestPathNormalisation(t *testing.T) {
	dir := t.TempDir()
	want := []byte("bcd-store")
	writeFile(t, dir, "Boot/BCD", want)
	addr := startServer(t, dir, Config{})

	for _, p := range []string{`\Boot\BCD`, `\boot\bcd`, `/BOOT/bcd`, `Boot//./BCD`} {
		res, err := (&client{}).get(addr, p)
		if err != nil {
			t.Errorf("%q: %v", p, err)
			continue
		}
		if !bytes.Equal(res.data, want) {
			t.Errorf("%q: wrong content", p)
		}
	}
}

func TestRefusals(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Boot/BCD", []byte("x"))
	outside := filepath.Join(filepath.Dir(dir), "secret-"+filepath.Base(dir))
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })
	addr := startServer(t, dir, Config{})

	cases := []struct {
		path string
		code uint16
	}{
		{"missing.efi", errFileNotFound},
		{"Boot", errFileNotFound}, // directory
		{`..\secret-` + filepath.Base(dir), errAccessViolation},
		{`C:\Windows\win.ini`, errAccessViolation},
		{`Boot\BCD:stream`, errAccessViolation},
	}
	for _, tc := range cases {
		_, err := (&client{}).get(addr, tc.path)
		var te *tftpError
		if !errors.As(err, &te) || te.code != tc.code {
			t.Errorf("%q: got %v, want error code %d", tc.path, err, tc.code)
		}
	}
}

func TestWriteRefused(t *testing.T) {
	addr := startServer(t, t.TempDir(), Config{})
	// Replies come from a new port (TID), so use an unconnected socket.
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	srv, _ := net.ResolveUDPAddr("udp4", addr)
	wrq := append([]byte{0, byte(opWRQ)}, "x\x00octet\x00"...)
	pc.WriteToUDP(wrq, srv)
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, _, err := pc.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if op := binary.BigEndian.Uint16(buf); op != opERROR {
		t.Fatalf("got op %d, want ERROR", op)
	}
	if code, _ := parseError(buf[:n]); code != errAccessViolation {
		t.Errorf("got code %d, want %d", code, errAccessViolation)
	}
}

func TestOptionNegotiation(t *testing.T) {
	dir := t.TempDir()
	want := randomBytes(100_000)
	writeFile(t, dir, "Boot/boot.wim", want)
	addr := startServer(t, dir, Config{MaxBlockSize: 1456, MaxWindowSize: 8})

	c := &client{opts: map[string]string{"blksize": "16384", "tsize": "0", "windowsize": "16", "timeout": "3"}}
	res, err := c.get(addr, `\Boot\boot.wim`)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.data, want) {
		t.Fatalf("content mismatch: got %d bytes", len(res.data))
	}
	wantOACK := map[string]string{"blksize": "1456", "tsize": "100000", "windowsize": "8", "timeout": "3"}
	for k, v := range wantOACK {
		if res.oack[k] != v {
			t.Errorf("OACK %s = %q, want %q", k, res.oack[k], v)
		}
	}
}

func TestTsizeProbeThenRead(t *testing.T) {
	dir := t.TempDir()
	want := randomBytes(5000)
	writeFile(t, dir, "bootmgfw.efi", want)
	addr := startServer(t, dir, Config{})

	res, err := (&client{opts: map[string]string{"tsize": "0"}, abortAfterOACK: true}).get(addr, "bootmgfw.efi")
	if err != nil {
		t.Fatal(err)
	}
	if res.oack["tsize"] != "5000" {
		t.Fatalf("tsize = %q", res.oack["tsize"])
	}
	res, err = (&client{opts: map[string]string{"blksize": "1456"}}).get(addr, "bootmgfw.efi")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.data, want) {
		t.Fatal("content mismatch after probe")
	}
}

// Sizes on block boundaries need a final empty DATA packet.
func TestExactMultipleOfBlockSize(t *testing.T) {
	dir := t.TempDir()
	for _, size := range []int{0, 512, 1024, 512 * 8} {
		name := fmt.Sprintf("f%d", size)
		want := randomBytes(size)
		writeFile(t, dir, name, want)
		addr := startServer(t, dir, Config{})
		for _, win := range []string{"1", "4"} {
			res, err := (&client{opts: map[string]string{"windowsize": win}}).get(addr, name)
			if err != nil {
				t.Fatalf("size %d window %s: %v", size, win, err)
			}
			if !bytes.Equal(res.data, want) {
				t.Fatalf("size %d window %s: got %d bytes", size, win, len(res.data))
			}
		}
	}
}

// boot.wim is hundreds of MB, so block numbers wrap past 65535.
func TestBlockNumberRollover(t *testing.T) {
	dir := t.TempDir()
	want := randomBytes(8*70_000 + 3) // 70,001 blocks of 8 bytes
	writeFile(t, dir, "big", want)
	addr := startServer(t, dir, Config{})

	res, err := (&client{opts: map[string]string{"blksize": "8", "windowsize": "64"}}).get(addr, "big")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.data, want) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(res.data), len(want))
	}
}

func TestRetransmitAfterLostACK(t *testing.T) {
	dir := t.TempDir()
	want := randomBytes(50_000)
	writeFile(t, dir, "f", want)
	addr := startServer(t, dir, Config{Timeout: 100 * time.Millisecond})

	dropped := map[int64]bool{}
	c := &client{
		opts: map[string]string{"blksize": "1024", "windowsize": "4"},
		// Drop the first ACK for blocks 8 and 20 once each.
		skipACK: func(b int64) bool {
			if (b == 8 || b == 20) && !dropped[b] {
				dropped[b] = true
				return true
			}
			return false
		},
	}
	res, err := c.get(addr, "f")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.data, want) {
		t.Fatal("content mismatch")
	}
	if !dropped[8] || !dropped[20] {
		t.Fatal("test did not drop the ACKs it meant to")
	}
}

func TestGivesUpWhenClientVanishes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f", randomBytes(10_000))
	addr := startServer(t, dir, Config{Timeout: 20 * time.Millisecond, Retries: 2})

	// Never ACK. The client read times out; the server must also stop, which
	// t.Cleanup checks by waiting for Serve to return.
	c := &client{skipACK: func(int64) bool { return true }, readTimeout: 500 * time.Millisecond}
	done := make(chan struct{})
	go func() {
		c.get(addr, "f")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("client did not finish")
	}
}

func TestParseRequest(t *testing.T) {
	r, err := parseRequest([]byte("\x00\x01\\Boot\\BCD\x00OCTET\x00BLKSIZE\x001456\x00tsize\x000\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if r.filename != `\Boot\BCD` || r.mode != "octet" || len(r.options) != 2 || r.options[0].name != "blksize" {
		t.Fatalf("unexpected parse: %+v", r)
	}
	for _, bad := range []string{"", "\x00\x01", "\x00\x01file", "\x00\x01\x00octet\x00", "\x00\x03\x00\x01"} {
		if _, err := parseRequest([]byte(bad)); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	dir := t.TempDir()
	for _, cfg := range []Config{
		{},
		{Root: filepath.Join(dir, "missing")},
		{Root: dir, MaxBlockSize: 4},
		{Root: dir, MaxWindowSize: 100_000},
	} {
		if s, err := New(cfg); err == nil {
			s.Close()
			t.Errorf("%+v: expected error", cfg)
		}
	}
}

// Package tftp is a read-only TFTP server for network boot.
//
// It implements RFC 1350 reads in octet mode with option negotiation
// (RFC 2347): blksize (RFC 2348), timeout and tsize (RFC 2349), and
// windowsize (RFC 7440). Windows Boot Manager uses windowsize to speed up
// RAM-disk downloads (BCD ramdisktftpwindowsize), which is why this is our
// own server rather than a library; see ADR 0012.
//
// Phase 1 spike: files come from a single root directory. Per-client file
// selection (ADR 0006) plugs in at Server.resolve.
package tftp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults for Config fields left at zero.
const (
	DefaultMaxBlockSize  = 65464 // largest blksize RFC 2348 allows
	DefaultMaxWindowSize = 64
	DefaultTimeout       = time.Second
	DefaultRetries       = 5

	minBlockSize    = 8
	plainBlockSize  = 512
	maxWindowLimit  = 4096 // keeps ACK numbers unambiguous across rollover
	readBufferBytes = 2048 // requests and ACKs are small
)

// Config configures a Server.
type Config struct {
	Root          string        // directory served as "/"; required
	MaxBlockSize  int           // cap on negotiated blksize
	MaxWindowSize int           // cap on negotiated windowsize
	Timeout       time.Duration // retransmit timeout unless the client negotiates one
	Retries       int           // consecutive timeouts before a transfer is abandoned
	Logger        *slog.Logger
}

// Server serves files read-only over TFTP.
type Server struct {
	cfg  Config
	root *os.Root
	log  *slog.Logger
	wg   sync.WaitGroup
}

// New validates cfg and opens the root directory.
func New(cfg Config) (*Server, error) {
	if cfg.Root == "" {
		return nil, errors.New("tftp: root directory is required")
	}
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("tftp: open root: %w", err)
	}
	if cfg.MaxBlockSize == 0 {
		cfg.MaxBlockSize = DefaultMaxBlockSize
	}
	if cfg.MaxBlockSize < minBlockSize || cfg.MaxBlockSize > DefaultMaxBlockSize {
		root.Close()
		return nil, fmt.Errorf("tftp: max block size %d outside %d..%d", cfg.MaxBlockSize, minBlockSize, DefaultMaxBlockSize)
	}
	if cfg.MaxWindowSize == 0 {
		cfg.MaxWindowSize = DefaultMaxWindowSize
	}
	if cfg.MaxWindowSize < 1 || cfg.MaxWindowSize > maxWindowLimit {
		root.Close()
		return nil, fmt.Errorf("tftp: max window size %d outside 1..%d", cfg.MaxWindowSize, maxWindowLimit)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Retries <= 0 {
		cfg.Retries = DefaultRetries
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{cfg: cfg, root: root, log: cfg.Logger.With("svc", "tftp")}, nil
}

// Close releases the root directory. Call it after Serve returns.
func (s *Server) Close() error { return s.root.Close() }

// ListenAndServe listens on addr (e.g. "0.0.0.0:69") and serves until ctx ends.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return fmt.Errorf("tftp: listen %s: %w", addr, err)
	}
	s.log.Info("tftp listener started", "addr", conn.LocalAddr().String(), "root", s.cfg.Root,
		"max_blksize", s.cfg.MaxBlockSize, "max_windowsize", s.cfg.MaxWindowSize)
	return s.Serve(ctx, conn)
}

// Serve reads requests from conn until ctx ends, then waits for running
// transfers to stop. Each transfer runs on its own socket (a new TID).
func (s *Server) Serve(ctx context.Context, conn net.PacketConn) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer s.wg.Wait()

	localIP := net.IPv4zero
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP != nil {
		localIP = a.IP
	}

	buf := make([]byte, readBufferBytes)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			// UDP read errors (e.g. an ICMP unreachable surfaced by the OS)
			// don't affect other clients. Keep serving.
			s.log.Warn("tftp read error", "err", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		remote, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, localIP, remote, pkt)
		}()
	}
}

// handle answers one request on a fresh socket.
func (s *Server) handle(ctx context.Context, localIP net.IP, remote *net.UDPAddr, pkt []byte) {
	log := s.log.With("client", remote.String())

	c, err := net.DialUDP("udp4", &net.UDPAddr{IP: localIP}, remote)
	if err != nil {
		log.Error("tftp transfer socket", "err", err)
		return
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	req, err := parseRequest(pkt)
	if err != nil {
		if len(pkt) >= 2 {
			op := binary.BigEndian.Uint16(pkt)
			if op != opRRQ && op != opWRQ {
				// Stray DATA/ACK/ERROR to port 69: not a new transfer.
				log.Debug("tftp ignoring non-request packet", "op", op)
				return
			}
		}
		log.Warn("tftp malformed request", "len", len(pkt))
		_, _ = c.Write(errorPacket(errIllegalOp, "malformed request"))
		return
	}
	log = log.With("path", req.filename)
	if req.op == opWRQ {
		log.Warn("tftp write refused")
		_, _ = c.Write(errorPacket(errAccessViolation, "read-only server"))
		return
	}
	if req.mode != "octet" && req.mode != "netascii" {
		log.Warn("tftp unsupported mode", "mode", req.mode)
		_, _ = c.Write(errorPacket(errIllegalOp, "unsupported mode"))
		return
	}

	t := &transfer{s: s, c: c, log: log, req: req, start: time.Now()}
	t.run()
}

// resolve maps a client path to a path under the root: "\" becomes "/",
// leading separators are dropped, and each component is matched
// case-insensitively (an exact match wins). ".." and ":" are refused.
func (s *Server) resolve(clientPath string) (string, error) {
	p := strings.ReplaceAll(clientPath, `\`, "/")
	var parts []string
	for _, part := range strings.Split(p, "/") {
		switch {
		case part == "" || part == ".":
			continue
		case part == "..":
			return "", errAccess
		case strings.ContainsAny(part, ":\x00"):
			// Drive letters and NTFS alternate data streams.
			return "", errAccess
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", errNotFound
	}
	exact := path.Join(parts...)
	if fi, err := s.root.Stat(exact); err == nil {
		if !fi.Mode().IsRegular() {
			return "", errNotFound
		}
		return exact, nil
	}

	cur := "."
	for i, part := range parts {
		d, err := s.root.Open(cur)
		if err != nil {
			return "", errNotFound
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return "", errNotFound
		}
		match := ""
		for _, e := range entries {
			if e.Name() == part {
				match = part
				break
			}
			if match == "" && strings.EqualFold(e.Name(), part) {
				match = e.Name()
			}
		}
		if match == "" {
			return "", errNotFound
		}
		cur = path.Join(cur, match)
		if i == len(parts)-1 {
			fi, err := s.root.Stat(cur)
			if err != nil || !fi.Mode().IsRegular() {
				return "", errNotFound
			}
		}
	}
	return cur, nil
}

var (
	errNotFound = errors.New("file not found")
	errAccess   = errors.New("access violation")
)

// transfer is one RRQ being served.
type transfer struct {
	s     *Server
	c     *net.UDPConn
	log   *slog.Logger
	req   request
	start time.Time

	blksize    int
	windowsize int
	timeout    time.Duration

	tsizeSent    bool // the OACK told the client the file size
	endedByTsize bool // see sendFile

	retransmits int
}

// clientError is an ERROR packet received from the client.
type clientError struct {
	code uint16
	msg  string
}

func (e *clientError) Error() string { return fmt.Sprintf("client error %d: %q", e.code, e.msg) }

func (t *transfer) run() {
	rel, err := t.s.resolve(t.req.filename)
	if err != nil {
		code := errFileNotFound
		if errors.Is(err, errAccess) {
			code = errAccessViolation
		}
		t.log.Warn("tftp request refused", "reason", err.Error(), "options", optionsAttr(t.req.options))
		_, _ = t.c.Write(errorPacket(code, err.Error()))
		return
	}
	f, err := t.s.root.Open(rel)
	if err != nil {
		t.log.Error("tftp open failed", "resolved", rel, "err", err)
		_, _ = t.c.Write(errorPacket(errNotDefined, "cannot open file"))
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.log.Error("tftp stat failed", "resolved", rel, "err", err)
		_, _ = t.c.Write(errorPacket(errNotDefined, "cannot open file"))
		return
	}
	size := fi.Size()

	accepted := t.negotiate(size)
	t.log = t.log.With("resolved", rel)
	t.log.Info("tftp request", "size", size, "options", optionsAttr(t.req.options),
		"blksize", t.blksize, "windowsize", t.windowsize, "timeout", t.timeout.String())

	if len(accepted) > 0 {
		if err := t.sendOACK(accepted); err != nil {
			var ce *clientError
			if errors.As(err, &ce) {
				// Common and expected: Windows Boot Manager and others ask
				// for tsize, read it from the OACK, then abort.
				t.log.Info("tftp client ended transfer after OACK", "code", ce.code, "msg", ce.msg)
				return
			}
			t.log.Warn("tftp transfer failed", "stage", "oack", "err", err)
			return
		}
	}

	blocks, err := t.sendFile(f, size)
	elapsed := time.Since(t.start)
	if err != nil {
		t.log.Warn("tftp transfer failed", "err", err, "blocks_acked", blocks,
			"retransmits", t.retransmits, "duration_ms", elapsed.Milliseconds())
		return
	}
	mbps := 0.0
	if s := elapsed.Seconds(); s > 0 {
		mbps = float64(size) / s / 1e6
	}
	end := "final-ack"
	if t.endedByTsize {
		end = "tsize" // client stopped at the size it was told, see sendFile
	}
	t.log.Info("tftp transfer complete", "bytes", size, "blocks", blocks,
		"retransmits", t.retransmits, "duration_ms", elapsed.Milliseconds(),
		"mb_per_s", strconv.FormatFloat(mbps, 'f', 1, 64), "end", end)
}

// negotiate applies the client's options and returns those to acknowledge.
func (t *transfer) negotiate(size int64) []option {
	t.blksize = plainBlockSize
	t.windowsize = 1
	t.timeout = t.s.cfg.Timeout
	var acc []option
	for _, o := range t.req.options {
		n, err := strconv.Atoi(o.value)
		switch o.name {
		case "blksize":
			if err != nil || n < minBlockSize {
				continue
			}
			t.blksize = min(n, t.s.cfg.MaxBlockSize)
			acc = append(acc, option{o.name, strconv.Itoa(t.blksize)})
		case "windowsize":
			if err != nil || n < 1 || n > 65535 {
				continue
			}
			t.windowsize = min(n, t.s.cfg.MaxWindowSize)
			acc = append(acc, option{o.name, strconv.Itoa(t.windowsize)})
		case "timeout":
			if err != nil || n < 1 || n > 255 {
				continue
			}
			t.timeout = time.Duration(n) * time.Second
			acc = append(acc, option{o.name, o.value})
		case "tsize":
			t.tsizeSent = true
			acc = append(acc, option{o.name, strconv.FormatInt(size, 10)})
		}
	}
	return acc
}

// sendOACK sends the option acknowledgement and waits for ACK 0.
func (t *transfer) sendOACK(opts []option) error {
	pkt := oackPacket(opts)
	buf := make([]byte, readBufferBytes)
	for attempt := 0; attempt <= t.s.cfg.Retries; attempt++ {
		if attempt > 0 {
			t.retransmits++
		}
		if _, err := t.c.Write(pkt); err != nil {
			return err
		}
		deadline := time.Now().Add(t.timeout)
		for {
			op, block, err := t.read(buf, deadline)
			if err != nil {
				if isTimeout(err) {
					break
				}
				return err
			}
			if op == opACK && block == 0 {
				return nil
			}
		}
	}
	return errors.New("no ACK for OACK")
}

// sendFile sends the file in windows of t.windowsize blocks. Block numbers
// are 16-bit on the wire and roll over to 0 after 65535. It returns the
// number of blocks acknowledged.
//
// When the size is a multiple of blksize, RFC 1350 ends the transfer with an
// empty block. Clients that were told the size (tsize) may stop once they
// have every byte and never ACK that empty block: Windows Boot Manager does
// this for boot.sdi (Phase 1 spike). If every data byte has been
// acknowledged and only the empty block is outstanding, a timeout or a
// closed client port counts as completion instead of retrying.
func (t *transfer) sendFile(f io.ReaderAt, size int64) (int64, error) {
	bs := int64(t.blksize)
	total := size/bs + 1 // the last block is short, possibly empty
	data := make([]byte, 4+t.blksize)
	rbuf := make([]byte, readBufferBytes)

	base := int64(1) // first unacknowledged block
	timeouts := 0
	for base <= total {
		end := min(base+int64(t.windowsize)-1, total)
		for b := base; b <= end; b++ {
			off := (b - 1) * bs
			n, err := f.ReadAt(data[4:], off)
			if err != nil && !errors.Is(err, io.EOF) {
				_, _ = t.c.Write(errorPacket(errNotDefined, "read error"))
				return base - 1, fmt.Errorf("read at %d: %w", off, err)
			}
			if _, err := t.c.Write(dataPacket(data, uint16(b), n)); err != nil {
				return base - 1, err
			}
		}

		acked, err := t.waitACK(rbuf, base-1, end)
		switch {
		case err == nil && acked >= base:
			base = acked + 1
			timeouts = 0
		case err == nil:
			// ACK of the block before the window: the client lost the
			// first block of the window. Resend from base.
			t.retransmits++
		case err != nil && t.tsizeSent && base == total && (base-1)*bs == size:
			// Timeout, or the client closed its port (ICMP unreachable).
			t.endedByTsize = true
			return base - 1, nil
		case isTimeout(err):
			timeouts++
			if timeouts > t.s.cfg.Retries {
				return base - 1, fmt.Errorf("no ACK after %d attempts (block %d)", timeouts, base)
			}
			t.retransmits++
		default:
			return base - 1, err
		}
	}
	return total, nil
}

// waitACK waits for an ACK that falls in [lo, hi] (absolute block numbers)
// and returns it. Older ACKs are ignored.
func (t *transfer) waitACK(buf []byte, lo, hi int64) (int64, error) {
	deadline := time.Now().Add(t.timeout)
	for {
		op, n, err := t.read(buf, deadline)
		if err != nil {
			return 0, err
		}
		if op != opACK {
			continue
		}
		// Map the 16-bit block number to the newest absolute block <= hi.
		abs := hi - int64(uint16(uint16(hi)-n))
		if abs >= lo {
			return abs, nil
		}
	}
}

// read returns the opcode and block number of the next packet. ERROR
// packets are returned as *clientError.
func (t *transfer) read(buf []byte, deadline time.Time) (uint16, uint16, error) {
	for {
		if err := t.c.SetReadDeadline(deadline); err != nil {
			return 0, 0, err
		}
		n, err := t.c.Read(buf)
		if err != nil {
			return 0, 0, err
		}
		if n < 4 {
			continue
		}
		op := binary.BigEndian.Uint16(buf)
		if op == opERROR {
			code, msg := parseError(buf[:n])
			return op, 0, &clientError{code, msg}
		}
		return op, binary.BigEndian.Uint16(buf[2:]), nil
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout() || errors.Is(err, os.ErrDeadlineExceeded)
}

func optionsAttr(opts []option) string {
	var sb strings.Builder
	for i, o := range opts {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(o.name)
		sb.WriteByte('=')
		sb.WriteString(o.value)
	}
	return sb.String()
}

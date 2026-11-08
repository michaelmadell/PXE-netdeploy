// Package boot holds what the network boot services share: the decision of
// what (if anything) a client boots, and the short-lived sessions that tie a
// client's IP to its MAC so TFTP can serve per-client files (ADR 0006,
// ADR 0013).
package boot

import (
	"context"
	"io/fs"
	"net/netip"
	"sync"
	"time"
)

// Arch is a client architecture, as in the API's ClientArch.
type Arch string

// Client architectures (DHCP option 93).
const (
	ArchBIOS          Arch = "bios"
	ArchUEFIX64       Arch = "uefi-x64"
	ArchUEFIX64HTTP   Arch = "uefi-x64-http"
	ArchUEFIArm64     Arch = "uefi-arm64"
	ArchUEFIArm64HTTP Arch = "uefi-arm64-http"
)

// HTTP reports whether a is a UEFI HTTP Boot architecture.
func (a Arch) HTTP() bool { return a == ArchUEFIX64HTTP || a == ArchUEFIArm64HTTP }

// Request is a network boot request that passed the DHCP filters.
type Request struct {
	Interface string
	MAC       string // normalised
	Arch      Arch
}

// Answer is what to send a client: a TFTP path for PXE, a URL for HTTP Boot,
// and the content TFTP and HTTP serve that client from (its boot image's
// tree, ADR 0015; nil means the server's default).
type Answer struct {
	File string
	URL  string
	Root fs.FS
	// TreeID is Root's content-store tree: kept while the session lives
	// (ADR 0024). Empty for content outside the store.
	TreeID string
	// For logs: the boot image, the profile and why that profile.
	BootImageID string
	ProfileID   string
	Source      string // "deployment", "client profile" or "default profile"
}

// LogAttrs describes the answer for logs.
func (a Answer) LogAttrs() []any {
	if a.BootImageID == "" {
		return nil
	}
	return []any{"boot_image", a.BootImageID, "profile", a.ProfileID, "via", a.Source}
}

// Booter decides what a client boots. ok=false means it gets no boot answer
// (it still gets an address in full mode); reason says why, for the log.
type Booter interface {
	Boot(ctx context.Context, r Request) (a Answer, ok bool, reason string)
}

// Static answers every UEFI x64 client with fixed boot content: protocol
// tests use it. The server decides with Profiles (ADR 0024).
type Static struct {
	File string // TFTP path for UEFI x64 PXE
	URL  string // URL for UEFI x64 HTTP Boot
	Root fs.FS  // content for every client
}

// Boot implements Booter.
func (s Static) Boot(_ context.Context, r Request) (Answer, bool, string) {
	switch r.Arch {
	case ArchUEFIX64:
		if s.File != "" {
			return Answer{File: s.File, Root: s.Root}, true, ""
		}
		return Answer{}, false, "no boot file configured"
	case ArchUEFIX64HTTP:
		if s.URL != "" {
			return Answer{URL: s.URL, Root: s.Root}, true, ""
		}
		return Answer{}, false, "no HTTP boot URL configured"
	}
	return Answer{}, false, "no boot content for " + string(r.Arch) + " yet"
}

// Session ties a client IP to the client that booted from it.
type Session struct {
	Interface string
	IP        netip.Addr
	MAC       string
	Arch      Arch
	Answer    Answer
	At        time.Time
}

// Sessions is the in-memory table of recent boots (ADR 0014: not stored; after
// a restart full mode falls back to the lease table, and a proxy-mode client
// retries). It also throttles how often a MAC's sighting is written. Safe for
// concurrent use.
type Sessions struct {
	TTL          time.Duration // session lifetime; zero means 10 minutes
	SightingGap  time.Duration // minimum gap between sightings; zero means 30 s
	Now          func() time.Time
	mu           sync.Mutex
	byIP         map[sessionKey]Session
	lastSighting map[string]sighting
}

type sessionKey struct {
	iface string
	ip    netip.Addr
}

func (s *Sessions) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sessions) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return 10 * time.Minute
}

// Record stores (or refreshes) the session for sess.IP on sess.Interface.
func (s *Sessions) Record(sess Session) {
	if !sess.IP.IsValid() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byIP == nil {
		s.byIP = map[sessionKey]Session{}
	}
	sess.At = s.now()
	s.byIP[sessionKey{sess.Interface, sess.IP}] = sess
	s.expireLocked()
}

// TreeIDs returns the content-store trees unexpired sessions serve, so
// garbage collection keeps them (ADR 0024).
func (s *Sessions) TreeIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, v := range s.byIP {
		if t := v.Answer.TreeID; t != "" && !seen[t] && s.now().Sub(v.At) <= s.ttl() {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Lookup returns the unexpired session for ip on iface.
func (s *Sessions) Lookup(iface string, ip netip.Addr) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byIP[sessionKey{iface, ip}]
	if !ok || s.now().Sub(sess.At) > s.ttl() {
		return Session{}, false
	}
	return sess, true
}

// SightingDue reports whether a sighting of mac should be written now, and
// if so notes that it was. It is due once SightingGap has passed, or straight
// away when ip is known and differs from the last written one: firmware sends
// DISCOVER (no address yet) and then REQUEST within seconds, and the address
// in the REQUEST must not be throttled away.
func (s *Sessions) SightingDue(mac, ip string) bool {
	gap := s.SightingGap
	if gap <= 0 {
		gap = 30 * time.Second
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSighting == nil {
		s.lastSighting = map[string]sighting{}
	}
	now := s.now()
	last, ok := s.lastSighting[mac]
	if ok && now.Sub(last.at) < gap && (ip == "" || ip == last.ip) {
		return false
	}
	if ip == "" {
		ip = last.ip
	}
	s.lastSighting[mac] = sighting{at: now, ip: ip}
	return true
}

type sighting struct {
	at time.Time
	ip string
}

// expireLocked drops old entries so the maps stay small. Callers hold s.mu.
func (s *Sessions) expireLocked() {
	now := s.now()
	for k, v := range s.byIP {
		if now.Sub(v.At) > s.ttl() {
			delete(s.byIP, k)
		}
	}
	for mac, t := range s.lastSighting {
		if now.Sub(t.at) > time.Hour {
			delete(s.lastSighting, mac)
		}
	}
}

package dhcp

import (
	"encoding/binary"
	"net"
	"time"
)

// offerHold is how long an offered address is reserved for a client that
// hasn't sent REQUEST yet.
const offerHold = 30 * time.Second

// declineHold keeps a declined address (address conflict) out of use.
const declineHold = 10 * time.Minute

type lease struct {
	ip      uint32
	mac     string
	expires time.Time
	bound   bool
}

// pool is an in-memory address pool. Leases are lost on restart until
// Phase 2 step 3 persists them. Not safe for concurrent use; Server
// serialises access.
type pool struct {
	start, end uint32
	byMAC      map[string]*lease
	byIP       map[uint32]*lease
	declined   map[uint32]time.Time
}

func newPool(start, end net.IP) *pool {
	return &pool{
		start:    ip4ToUint(start),
		end:      ip4ToUint(end),
		byMAC:    map[string]*lease{},
		byIP:     map[uint32]*lease{},
		declined: map[uint32]time.Time{},
	}
}

func (p *pool) clone() *pool {
	c := &pool{
		start:    p.start,
		end:      p.end,
		byMAC:    make(map[string]*lease, len(p.byMAC)),
		byIP:     make(map[uint32]*lease, len(p.byIP)),
		declined: make(map[uint32]time.Time, len(p.declined)),
	}
	for mac, l := range p.byMAC {
		cp := *l
		c.byMAC[mac] = &cp
		c.byIP[cp.ip] = &cp
	}
	for ip, until := range p.declined {
		c.declined[ip] = until
	}
	return c
}

func (p *pool) contains(ip uint32) bool { return ip >= p.start && ip <= p.end }

// free reports whether ip can be given to mac.
func (p *pool) free(ip uint32, mac string, now time.Time) bool {
	if !p.contains(ip) {
		return false
	}
	if until, ok := p.declined[ip]; ok && now.Before(until) {
		return false
	}
	l, ok := p.byIP[ip]
	return !ok || l.mac == mac || now.After(l.expires)
}

func (p *pool) set(ip uint32, mac string, expires time.Time, bound bool) {
	if old, ok := p.byMAC[mac]; ok && old.ip != ip {
		delete(p.byIP, old.ip)
	}
	if old, ok := p.byIP[ip]; ok && old.mac != mac {
		delete(p.byMAC, old.mac)
	}
	l := &lease{ip: ip, mac: mac, expires: expires, bound: bound}
	p.byMAC[mac] = l
	p.byIP[ip] = l
}

// offer picks an address for mac: its current lease, else the requested
// address if free, else the first free address. ok is false if the pool is
// exhausted.
func (p *pool) offer(mac string, requested net.IP, now time.Time) (net.IP, bool) {
	if l, ok := p.byMAC[mac]; ok && p.free(l.ip, mac, now) {
		if !l.bound || now.After(l.expires) {
			p.set(l.ip, mac, now.Add(offerHold), false)
		}
		return uintToIP4(l.ip), true
	}
	if r := ip4ToUint(requested); r != 0 && p.free(r, mac, now) {
		p.set(r, mac, now.Add(offerHold), false)
		return uintToIP4(r), true
	}
	for ip := p.start; ip <= p.end && ip >= p.start; ip++ {
		if p.free(ip, mac, now) {
			p.set(ip, mac, now.Add(offerHold), false)
			return uintToIP4(ip), true
		}
	}
	return nil, false
}

// bind commits ip to mac for d. It fails if ip is outside the pool or held
// by another client.
func (p *pool) bind(mac string, ip net.IP, d time.Duration, now time.Time) bool {
	u := ip4ToUint(ip)
	if !p.free(u, mac, now) {
		return false
	}
	p.set(u, mac, now.Add(d), true)
	return true
}

// release drops mac's lease, or its pending offer.
func (p *pool) release(mac string) {
	if l, ok := p.byMAC[mac]; ok {
		delete(p.byMAC, mac)
		delete(p.byIP, l.ip)
	}
}

func (p *pool) decline(ip net.IP, now time.Time) {
	u := ip4ToUint(ip)
	if !p.contains(u) {
		return
	}
	if l, ok := p.byIP[u]; ok {
		delete(p.byMAC, l.mac)
		delete(p.byIP, u)
	}
	p.declined[u] = now.Add(declineHold)
}

func ip4ToUint(ip net.IP) uint32 {
	v4 := ip.To4()
	if v4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(v4)
}

func uintToIP4(u uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, u)
	return ip
}

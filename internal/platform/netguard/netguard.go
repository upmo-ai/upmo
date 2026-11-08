// Package netguard is the SSRF dial guard every outbound HTTP client uses.
// It refuses to dial an address in a private,
// loopback, link-local, documentation or otherwise special range, and dials
// the vetted IP so a later DNS answer cannot change the target.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// ErrBlocked is returned, wrapped, when a dial is refused.
var ErrBlocked = errors.New("address blocked")

type blockedRange struct {
	prefix netip.Prefix
	reason string
}

var blocked4 = []blockedRange{
	{netip.MustParsePrefix("0.0.0.0/8"), "this network"},
	{netip.MustParsePrefix("10.0.0.0/8"), "private"},
	{netip.MustParsePrefix("100.100.100.200/32"), "metadata"},
	{netip.MustParsePrefix("100.64.0.0/10"), "shared address space"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local"},
	{netip.MustParsePrefix("172.16.0.0/12"), "private"},
	{netip.MustParsePrefix("192.0.0.0/24"), "protocol assignments"},
	{netip.MustParsePrefix("192.0.2.0/24"), "documentation"},
	{netip.MustParsePrefix("192.88.99.0/24"), "6to4 relay anycast"},
	{netip.MustParsePrefix("192.168.0.0/16"), "private"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking"},
	{netip.MustParsePrefix("198.51.100.0/24"), "documentation"},
	{netip.MustParsePrefix("203.0.113.0/24"), "documentation"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},
}

var blocked6 = []blockedRange{
	{netip.MustParsePrefix("::/128"), "unspecified"},
	{netip.MustParsePrefix("::1/128"), "loopback"},
	{netip.MustParsePrefix("100::/64"), "discard-only"},
	{netip.MustParsePrefix("2001::/32"), "teredo"},
	{netip.MustParsePrefix("2001:db8::/32"), "documentation"},
	{netip.MustParsePrefix("fd00:ec2::/32"), "metadata"},
	{netip.MustParsePrefix("fc00::/7"), "unique local"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("fec0::/10"), "site-local"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
}

// Prefixes whose addresses carry an IPv4 address. The IPv4 address is the
// low 32 bits for NAT64 and bits 16 to 47 for 6to4.
var (
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour  = netip.MustParsePrefix("2002::/16")
)

// BlockedAddr returns "" when addr may be dialed, or a short stable reason
// when it may not. An IPv4-mapped, NAT64 or 6to4 address is judged by the
// IPv4 address inside it and gets the same reason.
func BlockedAddr(addr netip.Addr) string {
	if !addr.IsValid() {
		return "invalid"
	}
	// A zoned address never matches a prefix, so judge it without the zone.
	addr = addr.WithZone("")
	if addr.Is4In6() {
		return BlockedAddr(addr.Unmap())
	}
	if addr.Is4() {
		return lookup(blocked4, addr)
	}
	b := addr.As16()
	switch {
	case nat64.Contains(addr), nat64Local.Contains(addr):
		return BlockedAddr(netip.AddrFrom4([4]byte(b[12:16])))
	case sixToFour.Contains(addr):
		return BlockedAddr(netip.AddrFrom4([4]byte(b[2:6])))
	}
	return lookup(blocked6, addr)
}

// BlockedAddrLocal is BlockedAddr for a target the owner chose to keep on
// their own network: loopback, private, shared and unique-local addresses
// may be dialed, and everything else BlockedAddr refuses stays refused. In
// particular link-local and cloud metadata addresses are never allowed.
func BlockedAddrLocal(addr netip.Addr) string {
	switch reason := BlockedAddr(addr); reason {
	case "private", "loopback", "shared address space", "unique local":
		return ""
	default:
		return reason
	}
}

func lookup(ranges []blockedRange, addr netip.Addr) string {
	for _, r := range ranges {
		if r.prefix.Contains(addr) {
			return r.reason
		}
	}
	return ""
}

// Guard dials only addresses BlockedAddr allows, unless Allow permits the
// host.
type Guard struct {
	Allow func(host string) bool // nil means nothing is allowed through a blocked address; called on every dial
	// LocalNetwork judges addresses with BlockedAddrLocal, so private and
	// loopback targets may be dialed but link-local and metadata never are.
	LocalNetwork bool
	// Lookup resolves a host. Nil uses the system resolver. Tests set it.
	Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dial resolves the host in addr to every IP and refuses with ErrBlocked if
// any of them is blocked and Allow does not permit the host. It then dials a
// resolved IP, never the hostname. Its signature matches
// http.Transport.DialContext.
func (g *Guard) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	ipNetwork, err := lookupNetwork(network)
	if err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	allowed := g.Allow != nil && g.Allow(host)
	lookupIP := net.DefaultResolver.LookupNetIP
	if g.Lookup != nil {
		lookupIP = g.Lookup
	}
	ips, err := lookupIP(ctx, ipNetwork, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("netguard: no address for %q", host)
	}
	if !allowed {
		judge := BlockedAddr
		if g.LocalNetwork {
			judge = BlockedAddrLocal
		}
		for _, ip := range ips {
			if reason := judge(ip); reason != "" {
				return nil, fmt.Errorf("netguard: %q resolves to %s (%s): %w", host, ip, reason, ErrBlocked)
			}
		}
	}
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	var errs []error
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// Transport returns an http.Transport that dials through g, with no proxy
// and finite timeouts. Every redirect hop dials through g too.
func (g *Guard) Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           g.Dial,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
}

func lookupNetwork(network string) (string, error) {
	switch network {
	case "tcp", "udp":
		return "ip", nil
	case "tcp4", "udp4":
		return "ip4", nil
	case "tcp6", "udp6":
		return "ip6", nil
	}
	return "", fmt.Errorf("netguard: unsupported network %q", network)
}

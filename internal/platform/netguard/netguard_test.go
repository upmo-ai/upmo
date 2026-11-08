package netguard

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBlockedAddr(t *testing.T) {
	tests := []struct {
		addr, want string
	}{
		{"0.0.0.0", "this network"},
		{"0.255.255.255", "this network"},
		{"1.0.0.0", ""},
		{"9.255.255.255", ""},
		{"10.0.0.0", "private"},
		{"10.255.255.255", "private"},
		{"11.0.0.0", ""},
		{"100.63.255.255", ""},
		{"100.64.0.0", "shared address space"},
		{"100.127.255.255", "shared address space"},
		{"100.128.0.0", ""},
		{"126.255.255.255", ""},
		{"127.0.0.1", "loopback"},
		{"127.255.255.255", "loopback"},
		{"128.0.0.0", ""},
		{"169.253.255.255", ""},
		{"169.254.0.0", "link-local"},
		{"169.254.169.254", "link-local"},
		{"169.255.0.0", ""},
		{"172.15.255.255", ""},
		{"172.16.0.0", "private"},
		{"172.31.255.255", "private"},
		{"172.32.0.0", ""},
		{"191.255.255.255", ""},
		{"192.0.0.0", "protocol assignments"},
		{"192.0.0.255", "protocol assignments"},
		{"192.0.1.0", ""},
		{"192.0.2.0", "documentation"},
		{"192.0.2.255", "documentation"},
		{"192.0.3.0", ""},
		{"192.88.98.255", ""},
		{"192.88.99.0", "6to4 relay anycast"},
		{"192.88.99.255", "6to4 relay anycast"},
		{"192.88.100.0", ""},
		{"192.167.255.255", ""},
		{"192.168.0.0", "private"},
		{"192.168.255.255", "private"},
		{"192.169.0.0", ""},
		{"198.17.255.255", ""},
		{"198.18.0.0", "benchmarking"},
		{"198.19.255.255", "benchmarking"},
		{"198.20.0.0", ""},
		{"198.51.99.255", ""},
		{"198.51.100.0", "documentation"},
		{"198.51.100.255", "documentation"},
		{"198.51.101.0", ""},
		{"203.0.112.255", ""},
		{"203.0.113.0", "documentation"},
		{"203.0.113.255", "documentation"},
		{"203.0.114.0", ""},
		{"223.255.255.255", ""},
		{"224.0.0.0", "multicast"},
		{"239.255.255.255", "multicast"},
		{"240.0.0.0", "reserved"},
		{"255.255.255.255", "reserved"},
		{"1.1.1.1", ""},
		{"8.8.8.8", ""},

		{"::", "unspecified"},
		{"::1", "loopback"},
		{"100::", "discard-only"},
		{"100::ffff:ffff:ffff:ffff", "discard-only"},
		{"100:0:0:1::", ""},
		{"2001::", "teredo"},
		{"2001:0:ffff:ffff:ffff:ffff:ffff:ffff", "teredo"},
		{"2001:db7:ffff:ffff:ffff:ffff:ffff:ffff", ""},
		{"2001:db8::", "documentation"},
		{"2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "documentation"},
		{"2001:db9::", ""},
		{"fc00::", "unique local"},
		{"fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "unique local"},
		{"fe80::", "link-local"},
		{"fe80::1%eth0", "link-local"},
		{"febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "link-local"},
		{"fec0::", "site-local"},
		{"feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "site-local"},
		{"ff00::", "multicast"},
		{"ff02::1", "multicast"},
		{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "multicast"},
		{"2001:4860:4860::8888", ""},
		{"2606:4700:4700::1111", ""},

		{"::ffff:1.1.1.1", ""},
		{"64:ff9b::101:101", ""},
		{"64:ff9b:1::101:101", ""},
		{"2002:101:101::", ""},
		{"2002:101:101:ffff:ffff:ffff:ffff:ffff", ""},
		{"::ffff:10.1.2.3", "private"},
		{"64:ff9b::a01:203", "private"},
		{"64:ff9b:1::a01:203", "private"},
		{"64:ff9b:1:ffff:ffff:ffff:a01:203", "private"},
		{"2002:a01:203::", "private"},
		{"2002:a01:203:ffff:ffff:ffff:ffff:ffff", "private"},
		{"::ffff:127.0.0.1", "loopback"},
		{"64:ff9b::a9fe:a9fe", "link-local"},
		{"2002:a9fe:a9fe::1", "link-local"},
		{"2002::", "this network"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := BlockedAddr(netip.MustParseAddr(tt.addr)); got != tt.want {
				t.Fatalf("BlockedAddr(%s) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestBlockedAddrInvalid(t *testing.T) {
	if got := BlockedAddr(netip.Addr{}); got == "" {
		t.Fatal("BlockedAddr(zero) allowed the zero address")
	}
}

// embedded returns the mapped, NAT64 and 6to4 forms of v4. noise fills the
// bits that do not carry the IPv4 address.
func embedded(v4 [4]byte, noise uint64) []netip.Addr {
	var mapped, n64, n64Local, six [16]byte
	mapped[10], mapped[11] = 0xff, 0xff
	copy(mapped[12:], v4[:])

	copy(n64[:], []byte{0x00, 0x64, 0xff, 0x9b})
	copy(n64[12:], v4[:])

	copy(n64Local[:], []byte{0x00, 0x64, 0xff, 0x9b, 0x00, 0x01})
	var noiseBytes [8]byte
	binary.LittleEndian.PutUint64(noiseBytes[:], noise)
	copy(n64Local[6:12], noiseBytes[:6])
	copy(n64Local[12:], v4[:])

	six[0], six[1] = 0x20, 0x02
	copy(six[2:6], v4[:])
	copy(six[6:14], noiseBytes[:])

	return []netip.Addr{
		netip.AddrFrom16(mapped),
		netip.AddrFrom16(n64),
		netip.AddrFrom16(n64Local),
		netip.AddrFrom16(six),
	}
}

func FuzzBlockedAddr(f *testing.F) {
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "10.1.2.3", "127.0.0.1", "169.254.169.254", "0.0.0.0", "255.255.255.255", "100.64.0.1"} {
		a := netip.MustParseAddr(s).As4()
		f.Add(a[0], a[1], a[2], a[3], uint64(0))
	}
	f.Add(byte(172), byte(31), byte(0), byte(1), ^uint64(0))
	f.Fuzz(func(t *testing.T, a, b, c, d byte, noise uint64) {
		v4 := [4]byte{a, b, c, d}
		want := BlockedAddr(netip.AddrFrom4(v4))
		for _, addr := range embedded(v4, noise) {
			if got := BlockedAddr(addr); got != want {
				t.Fatalf("BlockedAddr(%s) = %q, want %q as for %s", addr, got, want, netip.AddrFrom4(v4))
			}
		}
	})
}

func TestDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	allow := func(hosts ...string) func(string) bool {
		return func(h string) bool {
			for _, x := range hosts {
				if h == x {
					return true
				}
			}
			return false
		}
	}
	tests := []struct {
		name        string
		allow       func(string) bool
		network     string
		addr        string
		wantBlocked bool
		wantErr     bool
	}{
		{"loopback literal, nil allow", nil, "tcp", "127.0.0.1:" + port, true, true},
		{"loopback literal, other host allowed", allow("example.com"), "tcp", "127.0.0.1:" + port, true, true},
		{"mapped loopback literal", nil, "tcp", "[::ffff:127.0.0.1]:" + port, true, true},
		{"localhost name, nil allow", nil, "tcp", "localhost:" + port, true, true},
		{"metadata literal", nil, "tcp", "169.254.169.254:80", true, true},
		{"loopback literal allowed", allow("127.0.0.1"), "tcp", "127.0.0.1:" + port, false, false},
		{"localhost name allowed", allow("localhost"), "tcp4", "localhost:" + port, false, false},
		{"missing port", nil, "tcp", "127.0.0.1", false, true},
		{"unsupported network", nil, "unix", "/tmp/sock", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Guard{Allow: tt.allow}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := g.Dial(ctx, tt.network, tt.addr)
			if conn != nil {
				defer func() { _ = conn.Close() }()
			}
			if got := errors.Is(err, ErrBlocked); got != tt.wantBlocked {
				t.Fatalf("errors.Is(%v, ErrBlocked) = %v, want %v", err, got, tt.wantBlocked)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("Dial error = %v, want error %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			// The guard dials the resolved IP, never the name.
			if got := conn.RemoteAddr().String(); got != "127.0.0.1:"+port {
				t.Fatalf("RemoteAddr = %s, want 127.0.0.1:%s", got, port)
			}
		})
	}
}

func TestDialCallsAllowEveryTime(t *testing.T) {
	var calls atomic.Int32
	g := &Guard{Allow: func(string) bool { calls.Add(1); return false }}
	for range 3 {
		if _, err := g.Dial(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, ErrBlocked) {
			t.Fatalf("Dial error = %v, want ErrBlocked", err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("Allow called %d times, want 3", n)
	}
}

func TestTransport(t *testing.T) {
	tr := (&Guard{}).Transport()
	if tr.Proxy != nil {
		t.Error("Proxy is set, want nil")
	}
	if tr.DialContext == nil {
		t.Error("DialContext is nil")
	}
	if tr.DialTLSContext != nil {
		t.Error("a TLS dialer bypasses the guard")
	}
	for name, d := range map[string]time.Duration{
		"TLSHandshakeTimeout":   tr.TLSHandshakeTimeout,
		"ResponseHeaderTimeout": tr.ResponseHeaderTimeout,
		"IdleConnTimeout":       tr.IdleConnTimeout,
	} {
		if d <= 0 {
			t.Errorf("%s = %v, want finite and positive", name, d)
		}
	}
}

func TestTransportRedirect(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		allow    func(string) bool
		wantHits int32
	}{
		{"server not allowed", nil, 0},
		{"server allowed", func(h string) bool { return h == u.Hostname() }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits.Store(0)
			var mu sync.Mutex
			var asked []string
			g := &Guard{Allow: func(h string) bool {
				mu.Lock()
				asked = append(asked, h)
				mu.Unlock()
				return tt.allow != nil && tt.allow(h)
			}}
			client := &http.Client{Transport: g.Transport(), Timeout: 10 * time.Second}
			resp, err := client.Get(srv.URL)
			if err == nil {
				_ = resp.Body.Close()
				t.Fatal("Get succeeded, want ErrBlocked")
			}
			if !errors.Is(err, ErrBlocked) {
				t.Fatalf("Get error = %v, want ErrBlocked", err)
			}
			if n := hits.Load(); n != tt.wantHits {
				t.Fatalf("server hits = %d, want %d", n, tt.wantHits)
			}
			mu.Lock()
			defer mu.Unlock()
			if tt.wantHits == 1 && (len(asked) != 2 || asked[1] != "169.254.169.254") {
				t.Fatalf("Allow asked %q, want the server then 169.254.169.254", asked)
			}
		})
	}
}

func TestBlockedAddrMetadata(t *testing.T) {
	for addr, want := range map[string]string{
		"100.100.100.200": "metadata",
		"100.100.100.201": "shared address space",
		"fd00:ec2::254":   "metadata",
		"fd00:ec3::254":   "unique local",
	} {
		if got := BlockedAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("BlockedAddr(%s) = %q, want %q", addr, got, want)
		}
	}
}

func TestBlockedAddrLocal(t *testing.T) {
	tests := []struct {
		addr, want string
	}{
		{"127.0.0.1", ""},
		{"::1", ""},
		{"10.1.2.3", ""},
		{"172.20.0.5", ""},
		{"192.168.1.9", ""},
		{"100.64.0.7", ""},
		{"fd12:3456::1", ""},
		{"::ffff:10.0.0.1", ""},
		{"8.8.8.8", ""},
		{"169.254.169.254", "link-local"},
		{"169.254.170.2", "link-local"},
		{"::ffff:169.254.169.254", "link-local"},
		{"64:ff9b::a9fe:a9fe", "link-local"},
		{"fe80::1", "link-local"},
		{"100.100.100.200", "metadata"},
		{"fd00:ec2::254", "metadata"},
		{"0.0.0.0", "this network"},
		{"::", "unspecified"},
		{"224.0.0.1", "multicast"},
		{"192.0.2.1", "documentation"},
	}
	for _, tt := range tests {
		if got := BlockedAddrLocal(netip.MustParseAddr(tt.addr)); got != tt.want {
			t.Errorf("BlockedAddrLocal(%s) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestDialLocalNetworkAndLookup(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	table := map[string][]netip.Addr{
		"home.test":     {netip.MustParseAddr("127.0.0.1")},
		"rebind.test":   {netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("169.254.169.254")},
		"metadata.test": {netip.MustParseAddr("169.254.169.254")},
	}
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if ips, ok := table[host]; ok {
			return ips, nil
		}
		return nil, errors.New("no such host")
	}
	tests := []struct {
		name  string
		local bool
		host  string
		block bool
	}{
		{"strict refuses a name that resolves to loopback", false, "home.test", true},
		{"local dials a name that resolves to loopback", true, "home.test", false},
		{"local refuses a name with one metadata answer", true, "rebind.test", true},
		{"local refuses a name that resolves to metadata", true, "metadata.test", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Guard{LocalNetwork: tt.local, Lookup: lookup}
			conn, err := g.Dial(context.Background(), "tcp", net.JoinHostPort(tt.host, port))
			if conn != nil {
				_ = conn.Close()
			}
			if got := errors.Is(err, ErrBlocked); got != tt.block {
				t.Fatalf("Dial error = %v, want blocked %v", err, tt.block)
			}
			if !tt.block && err != nil {
				t.Fatal(err)
			}
		})
	}
}

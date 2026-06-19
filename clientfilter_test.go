package gateway

import (
	"context"
	"encoding/base64"
	"net"
	"net/netip"
	"testing"

	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/pkg/fall"
	"github.com/coredns/coredns/plugin/test"
	"github.com/miekg/dns"
)

// msgWithECS builds a DNS A query msg carrying an EDNS0 Client Subnet option.
// The SourceNetmask is set to 32: with Blocky forwarding ipv4Mask:32 the full
// client IP is carried and only its Address is consumed by filterAddressesByClientSubnet.
func msgWithECS(t *testing.T, family uint16, addr string, netmask uint8) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion("code.dev.example.com.", dns.TypeA)
	m.SetEdns0(4096, false)
	opt := m.IsEdns0()
	if opt == nil {
		t.Fatalf("IsEdns0 returned nil after SetEdns0")
	}
	opt.Option = append(opt.Option, &dns.EDNS0_SUBNET{
		Code:          dns.EDNS0SUBNET,
		Family:        family,
		SourceNetmask: netmask,
		Address:       net.ParseIP(addr),
	})
	return m
}

// msgWithoutECS builds a DNS A query msg with EDNS0 but no ECS option.
func msgWithoutECS() *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion("code.dev.example.com.", dns.TypeA)
	m.SetEdns0(4096, false)
	return m
}

func mustAddr(s string) netip.Addr {
	return netip.MustParseAddr(s)
}

func addrsToStrings(a []netip.Addr) []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = x.String()
	}
	return out
}

// lookupFromMap builds a nodeSubnetLookupFunc from a map of interface IP
// (string form, e.g. "192.168.15.112") → "*net.IPNet" (e.g. "192.168.15.0/24").
// Candidates not present in the map return nil (fail-open), emulating VIPs.
func lookupFromMap(m map[string]string) nodeSubnetLookupFunc {
	return func(candidate netip.Addr) *net.IPNet {
		s := candidate.String()
		val, ok := m[s]
		if !ok {
			return nil
		}
		_, ipn, err := net.ParseCIDR(val)
		if err != nil {
			return nil
		}
		return ipn
	}
}

func TestFilterAddressesByClientSubnet(t *testing.T) {
	// Node interface subnets for the test:
	//   192.168.13.137 → 192.168.13.0/24  (rpi1000 eth0, LAN)
	//   192.168.15.112 → 192.168.15.0/24  (sensecap-m4 wlan0, LAN)
	//   172.28.110.103 → 172.28.0.0/16   (zerotier ztrtavkx6v)
	// VIPs (172.28.10.1, fd12:...) are absent → fail-open.
	nodeInterfaces := map[string]string{
		"192.168.13.137":      "192.168.13.0/24",
		"192.168.15.112":      "192.168.15.0/24",
		"172.28.110.103":      "172.28.0.0/16",
		"fd12:3456:789a:1::1": "fd12:3456:789a:1::/64",
	}

	tests := []struct {
		name   string
		req    *dns.Msg
		in     []netip.Addr
		lookup nodeSubnetLookupFunc
		want   []string
	}{
		{
			name:   "LAN client keeps only same-site LAN IP, filters other-site LAN",
			req:    msgWithECS(t, 1, "192.168.13.167", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"192.168.13.137", "172.28.10.1"}, // 172.28.10.1 is VIP → fail-open
		},
		{
			name:   "zerotier client filters out LAN candidates, keeps VIP",
			req:    msgWithECS(t, 1, "172.28.110.103", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"172.28.10.1"},
		},
		{
			name:   "zerotier client /16 keeps zerotier interface IP and VIP, filters LAN",
			req:    msgWithECS(t, 1, "172.28.110.103", 32),
			in:     []netip.Addr{mustAddr("172.28.110.103"), mustAddr("192.168.15.112"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"172.28.110.103", "172.28.10.1"},
		},
		{
			name:   "wLAN client keeps wlan-site IP only",
			req:    msgWithECS(t, 1, "192.168.15.42", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"192.168.15.112"},
		},
		{
			name:   "roaming client not in any node subnet → only VIPs kept",
			req:    msgWithECS(t, 1, "8.8.8.8", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"172.28.10.1"},
		},
		{
			name:   "ECS absent → unchanged (fail-open)",
			req:    msgWithoutECS(),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"192.168.13.137", "192.168.15.112", "172.28.10.1"},
		},
		{
			name:   "lookup nil (no Node informer / annotation missing) → fail-open, all kept",
			req:    msgWithECS(t, 1, "192.168.13.167", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112")},
			lookup: nil,
			want:   []string{"192.168.13.137", "192.168.15.112"},
		},
		{
			name:   "lookup returns nil for every candidate (annotation empty) → all kept",
			req:    msgWithECS(t, 1, "192.168.13.167", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(map[string]string{}),
			want:   []string{"192.168.13.137", "172.28.10.1"},
		},
		{
			name:   "IPv6 client matches IPv6 interface",
			req:    msgWithECS(t, 2, "fd12:3456:789a:1::99", 128),
			in:     []netip.Addr{mustAddr("fd12:3456:789a:1::1"), mustAddr("192.168.13.137")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"fd12:3456:789a:1::1"},
		},
		{
			name:   "empty input → empty output",
			req:    msgWithECS(t, 1, "192.168.13.167", 32),
			in:     []netip.Addr{},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{},
		},
		{
			name:   "ECS with zero netmask but valid address → still uses address (mask ignored)",
			req:    msgWithECS(t, 1, "192.168.13.167", 0),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"192.168.13.137"},
		},
		{
			name:   "ECS with unknown family → no usable address, fail-open",
			req:    msgWithECS(t, 3, "192.168.13.167", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"192.168.13.137", "192.168.15.112"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterAddressesByClientSubnet(tc.req, tc.in, tc.lookup, "failOpen")
			gotStrs := addrsToStrings(got)
			if len(gotStrs) != len(tc.want) {
				t.Fatalf("len mismatch: got %d (%v), want %d (%v)", len(gotStrs), gotStrs, len(tc.want), tc.want)
			}
			for i := range gotStrs {
				if gotStrs[i] != tc.want[i] {
					t.Errorf("idx %d: got %s, want %s", i, gotStrs[i], tc.want[i])
				}
			}
		})
	}
}

// TestFilterAddressesByClientSubnetStrictMode verifies that strict mode drops
// candidates whose subnet is unknown (VIPs / aliases) while keeping path 1
// (no ECS) and path 2 (lookup nil) fail-open for safety.
func TestFilterAddressesByClientSubnetStrictMode(t *testing.T) {
	nodeInterfaces := map[string]string{
		"192.168.13.137": "192.168.13.0/24",
		"192.168.15.112": "192.168.15.0/24",
	}

	tests := []struct {
		name   string
		req    *dns.Msg
		in     []netip.Addr
		lookup nodeSubnetLookupFunc
		want   []string
	}{
		{
			name:   "LAN client drops VIP, keeps matching LAN",
			req:    msgWithECS(t, 1, "192.168.13.167", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"192.168.13.137"},
		},
		{
			name:   "zerotier client drops LAN and VIP",
			req:    msgWithECS(t, 1, "172.28.110.103", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{},
		},
		{
			name:   "roaming client drops everything",
			req:    msgWithECS(t, 1, "8.8.8.8", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{},
		},
		{
			name:   "ECS absent remains fail-open even in strict mode",
			req:    msgWithoutECS(),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("172.28.10.1")},
			lookup: lookupFromMap(nodeInterfaces),
			want:   []string{"192.168.13.137", "172.28.10.1"},
		},
		{
			name:   "lookup nil remains fail-open even in strict mode",
			req:    msgWithECS(t, 1, "192.168.13.167", 32),
			in:     []netip.Addr{mustAddr("192.168.13.137"), mustAddr("192.168.15.112")},
			lookup: nil,
			want:   []string{"192.168.13.137", "192.168.15.112"},
		},
		{
			name:   "all candidates are VIPs → empty",
			req:    msgWithECS(t, 1, "192.168.13.167", 32),
			in:     []netip.Addr{mustAddr("172.28.10.1"), mustAddr("10.0.0.1")},
			lookup: lookupFromMap(map[string]string{}),
			want:   []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterAddressesByClientSubnet(tc.req, tc.in, tc.lookup, "strict")
			gotStrs := addrsToStrings(got)
			if len(gotStrs) != len(tc.want) {
				t.Fatalf("len mismatch: got %d (%v), want %d (%v)", len(gotStrs), gotStrs, len(tc.want), tc.want)
			}
			for i := range gotStrs {
				if gotStrs[i] != tc.want[i] {
					t.Errorf("idx %d: got %s, want %s", i, gotStrs[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseInterfaceAnnotation verifies the base64 annotation body produced by
// the interface-exporter DaemonSet (`ip -o -4 addr show | awk '{print $2"\t"$4}'`)
// parses correctly into ifaceEntry slices, including the fail-open cases.
func TestParseInterfaceAnnotation(t *testing.T) {
	// Decoded body:
	//   wlan0\t192.168.15.112/24
	//   ztrtavkx6v\t172.28.110.103/16
	//   cbr0\t10.42.4.1/24
	body := "wlan0\t192.168.15.112/24\nztrtavkx6v\t172.28.110.103/16\ncbr0\t10.42.4.1/24\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(body))

	entries := parseInterfaceAnnotation(encoded)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d (%v)", len(entries), entries)
	}
	want := []struct {
		ifaceIP string
		subnet  string
	}{
		{"192.168.15.112", "192.168.15.0/24"},
		{"172.28.110.103", "172.28.0.0/16"},
		{"10.42.4.1", "10.42.4.0/24"},
	}
	for i, e := range entries {
		if !e.ifaceIP.Equal(net.ParseIP(want[i].ifaceIP)) {
			t.Errorf("entry %d: ifaceIP got %s, want %s", i, e.ifaceIP, want[i].ifaceIP)
		}
		if e.subnet == nil || e.subnet.String() != want[i].subnet {
			t.Errorf("entry %d: subnet got %v, want %s", i, e.subnet, want[i].subnet)
		}
	}

	// Malformed input must fail-open (return nil), not panic.
	if got := parseInterfaceAnnotation("!!! not base64 !!!"); got != nil {
		t.Errorf("expected nil for invalid base64, got %v", got)
	}
	// Empty / whitespace-only body → nil.
	if got := parseInterfaceAnnotation(base64.StdEncoding.EncodeToString([]byte("\n\n"))); got != nil {
		t.Errorf("expected nil for whitespace-only body, got %v", got)
	}
	// Garbage CIDR in a line → that line skipped, others kept.
	mixed := "wlan0\t192.168.15.112/24\nlo\tgarbage\neth0\t10.0.0.1/8\n"
	mixedEnc := base64.StdEncoding.EncodeToString([]byte(mixed))
	got := parseInterfaceAnnotation(mixedEnc)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after skipping garbage line, got %d (%v)", len(got), got)
	}
	if !got[0].ifaceIP.Equal(net.ParseIP("192.168.15.112")) {
		t.Errorf("first entry ifaceIP got %s, want 192.168.15.112", got[0].ifaceIP)
	}
	// /31 and /32 entries (host routes / aliases) must be skipped.
	with32 := "lo\t127.0.0.1/8\nzt\t172.28.10.1/32\nzt\t172.28.110.103/16\n"
	with32Enc := base64.StdEncoding.EncodeToString([]byte(with32))
	got32 := parseInterfaceAnnotation(with32Enc)
	if len(got32) != 2 {
		t.Fatalf("expected 2 entries after skipping /32, got %d (%v)", len(got32), got32)
	}
	if !got32[0].ifaceIP.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("first entry ifaceIP got %s, want 127.0.0.1", got32[0].ifaceIP)
	}
	if !got32[1].ifaceIP.Equal(net.ParseIP("172.28.110.103")) {
		t.Errorf("second entry ifaceIP got %s, want 172.28.110.103", got32[1].ifaceIP)
	}
}

// TestServeDNS_ClientFilteringFallthrough exercises the full ServeDNS path:
// when clientFiltering strips all addresses and Fall is configured, the
// request falls through to the next plugin instead of returning NXDOMAIN.
func TestServeDNS_ClientFilteringFallthrough(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}
	gw := newGateway()
	gw.Zones = []string{"example.com."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, nil)
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	gw.clientFiltering = true
	gw.Fall = fall.F{Zones: []string{"."}}
	setupLookupFuncs(gw)
	// No nodeInterfaceLookup wired → fail-open behavior is to keep
	// candidates unchanged, so we must pick a client IP that does NOT
	// match the only candidate (192.0.0.1) under any lookup to force
	// emptying. With lookup nil, filterAddressesByClientSubnet keeps all,
	// so to exercise the fallthrough path we instead wire a lookup that
	// returns a subnet that does not contain the client IP.
	gw.nodeInterfaceLookup = lookupFromMap(map[string]string{
		"192.0.0.1": "10.0.0.0/24",
	})

	// domain.example.com resolves to 192.0.0.1 in the ingress index
	// (see gateway_test.go). Client 8.8.8.8 is not in 10.0.0.0/24 → filtered
	// out → fall through to next handler.
	m := msgWithECS(t, 1, "8.8.8.8", 32)
	m.Question[0].Name = "domain.example.com."
	w := dnstest.NewRecorder(&test.ResponseWriter{})
	rcode, err := gw.ServeDNS(context.TODO(), w, m)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	// Fallthrough forwards to next handler, which returns RcodeSuccess
	// without writing a response (test.NextHandler doesn't call WriteMsg).
	if rcode != dns.RcodeSuccess {
		t.Fatalf("expected fallthrough RcodeSuccess, got %d", rcode)
	}
	// No response written by the fallthrough next handler.
	if w.Msg != nil && len(w.Msg.Answer) != 0 {
		t.Fatalf("expected no answer after Fall, got %v", w.Msg.Answer)
	}
}

// TestServeDNS_ClientFilteringKeepsMatching verifies the happy path: when
// clientFiltering is on and the ECS client IP falls in the node interface
// subnet of the candidate IP, that candidate is returned in the answer.
func TestServeDNS_ClientFilteringKeepsMatching(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}
	gw := newGateway()
	gw.Zones = []string{"example.com."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, nil)
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	gw.clientFiltering = true
	setupLookupFuncs(gw)
	gw.nodeInterfaceLookup = lookupFromMap(map[string]string{
		"192.0.0.1": "192.0.0.0/24",
	})

	// domain.example.com → 192.0.0.1 (ingress index). Client 192.0.0.42 is in
	// 192.0.0.0/24 → candidate kept.
	m := msgWithECS(t, 1, "192.0.0.42", 32)
	m.Question[0].Name = "domain.example.com."
	w := dnstest.NewRecorder(&test.ResponseWriter{})
	_, err := gw.ServeDNS(context.TODO(), w, m)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if w.Msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected RcodeSuccess, got %d", w.Msg.Rcode)
	}
	if len(w.Msg.Answer) != 1 {
		t.Fatalf("expected exactly 1 answer after filter, got %d (%v)", len(w.Msg.Answer), w.Msg.Answer)
	}
}

// TestServeDNS_ClientFilteringNilLookupFailOpen verifies that when no Node
// informer is wired (e.g. Node resource not configured), clientFiltering
// falls open: all candidates are kept.
func TestServeDNS_ClientFilteringNilLookupFailOpen(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}
	gw := newGateway()
	gw.Zones = []string{"example.com."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, nil)
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	gw.clientFiltering = true
	setupLookupFuncs(gw)
	// gw.nodeInterfaceLookup left as nil.

	m := msgWithECS(t, 1, "8.8.8.8", 32)
	m.Question[0].Name = "domain.example.com."
	w := dnstest.NewRecorder(&test.ResponseWriter{})
	_, err := gw.ServeDNS(context.TODO(), w, m)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if w.Msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected RcodeSuccess, got %d", w.Msg.Rcode)
	}
	if len(w.Msg.Answer) != 1 {
		t.Fatalf("expected 1 answer (fail-open kept the candidate), got %d (%v)", len(w.Msg.Answer), w.Msg.Answer)
	}
}

package gateway

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/coredns/coredns/plugin/pkg/fall"
	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/test"

	"github.com/miekg/dns"
)

// msgWithECS builds a DNS A query msg carrying an EDNS0 Client Subnet option.
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

func TestFilterAddressesByClientSubnet(t *testing.T) {
	// Candidate address pool: mix of two /24 IPv4 sites + one IPv6.
	all := []netip.Addr{
		mustAddr("192.168.13.137"),
		mustAddr("192.168.15.137"),
		mustAddr("fd12:3456:789a:1::1"),
	}

	tests := []struct {
		name string
		req  *dns.Msg
		in   []netip.Addr
		want []string
	}{
		{
			name: "ECS present & matching /24 keeps only matching site",
			req:  msgWithECS(t, 1, "192.168.13.0", 24),
			in:   all,
			want: []string{"192.168.13.137"},
		},
		{
			name: "ECS present & matching other /24 keeps only that site",
			req:  msgWithECS(t, 1, "192.168.15.0", 24),
			in:   all,
			want: []string{"192.168.15.137"},
		},
		{
			name: "ECS present & no match → empty (caller falls through)",
			req:  msgWithECS(t, 1, "10.0.0.0", 24),
			in:   all,
			want: []string{},
		},
		{
			name: "ECS absent → unchanged (fail open)",
			req:  msgWithoutECS(),
			in:   all,
			want: []string{"192.168.13.137", "192.168.15.137", "fd12:3456:789a:1::1"},
		},
		{
			name: "ECS IPv6 /48 keeps only the IPv6 addr in that prefix",
			req:  msgWithECS(t, 2, "fd12:3456:789a:1::", 48),
			in:   all,
			want: []string{"fd12:3456:789a:1::1"},
		},
		{
			name: "ECS IPv4 /16 keeps both 192.168.x addresses",
			req:  msgWithECS(t, 1, "192.168.0.0", 16),
			in:   all,
			want: []string{"192.168.13.137", "192.168.15.137"},
		},
		{
			name: "ECS with zero netmask → malformed, fail open (unchanged)",
			req:  msgWithECS(t, 1, "192.168.13.0", 0),
			in:   all,
			want: []string{"192.168.13.137", "192.168.15.137", "fd12:3456:789a:1::1"},
		},
		{
			name: "ECS with unknown family → malformed, fail open (unchanged)",
			req:  msgWithECS(t, 3, "192.168.13.0", 24),
			in:   all,
			want: []string{"192.168.13.137", "192.168.15.137", "fd12:3456:789a:1::1"},
		},
		{
			name: "ECS netmask /32 keeps only exact IP",
			req:  msgWithECS(t, 1, "192.168.13.137", 32),
			in:   all,
			want: []string{"192.168.13.137"},
		},
		{
			name: "empty input → empty output",
			req:  msgWithECS(t, 1, "192.168.13.0", 24),
			in:   []netip.Addr{},
			want: []string{},
		},
		{
			name: "ECS netmask larger than address bits → malformed, fail open",
			req:  msgWithECS(t, 1, "192.168.13.0", 33),
			in:   all,
			want: []string{"192.168.13.137", "192.168.15.137", "fd12:3456:789a:1::1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterAddressesByClientSubnet(tc.req, tc.in)
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

	// domain.example.com resolves to 192.0.0.1 in the ingress index
	// (see gateway_test.go). ECS 10.20.30.0/24 must strip it and fall through.
	m := msgWithECS(t, 1, "10.20.30.0", 24)
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
// clientFiltering is on and the ECS subnet matches one of the candidate IPs,
// only that IP is returned in the answer.
func TestServeDNS_ClientFilteringKeepsMatching(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}
	gw := newGateway()
	gw.Zones = []string{"example.com."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, nil)
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	gw.clientFiltering = true
	setupLookupFuncs(gw)

	// domain.example.com → 192.0.0.1 (ingress index). Use ECS that includes it.
	m := msgWithECS(t, 1, "192.0.0.0", 24)
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

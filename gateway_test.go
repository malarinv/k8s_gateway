package gateway

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/coredns/coredns/plugin/pkg/fall"

	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/test"

	"github.com/miekg/dns"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

type FallthroughCase struct {
	test.Case
	FallthroughZones    []string
	FallthroughExpected bool
}

type Fallen struct {
	error
}

func TestLookup(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}

	gw := newGateway()
	gw.Zones = []string{"example.com."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, nil)
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	real := []string{"Ingress", "Service", "HTTPRoute", "TLSRoute", "GRPCRoute", "DNSEndpoint"}
	fake := []string{"Pod", "Gateway"}

	for _, resource := range real {
		if found := gw.lookupResource(resource); found == nil {
			t.Errorf("Could not lookup supported resource %s", resource)
		}
	}

	for _, resource := range fake {
		if found := gw.lookupResource(resource); found != nil {
			t.Errorf("Located unsupported resource %s", resource)
		}
	}
}

func TestPlugin(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}

	gw := newGateway()
	gw.Zones = []string{"example.com."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, nil)
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	setupLookupFuncs(gw)

	ctx := context.TODO()
	for i, tc := range tests {
		r := tc.Msg()
		w := dnstest.NewRecorder(&test.ResponseWriter{})

		_, err := gw.ServeDNS(ctx, w, r)
		if err != tc.Error {
			t.Errorf("Test %d expected no error, got %v", i, err)
			return
		}
		if tc.Error != nil {
			continue
		}

		resp := w.Msg

		if resp == nil {
			t.Fatalf("Test %d, got nil message and no error for %q", i, r.Question[0].Name)
		}
		if err = test.SortAndCheck(resp, tc); err != nil {
			t.Errorf("Test %d failed with error: %v", i, err)
		}
	}
}

func TestPluginFallthrough(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}
	gw := newGateway()
	gw.Zones = []string{"example.com."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, Fallen{})
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	setupLookupFuncs(gw)

	ctx := context.TODO()
	for i, tc := range testsFallthrough {
		r := tc.Msg()
		w := dnstest.NewRecorder(&test.ResponseWriter{})

		gw.Fall = fall.F{Zones: tc.FallthroughZones}
		_, err := gw.ServeDNS(ctx, w, r)

		if errors.As(err, &Fallen{}) && !tc.FallthroughExpected {
			t.Fatalf("Test %d query resulted unexpectedly in a fall through instead of a response", i)
		}
		if err == nil && tc.FallthroughExpected {
			t.Fatalf("Test %d query resulted unexpectedly in a response instead of a fall through", i)
		}
	}
}

var tests = []test.Case{
	// Existing Service IPv4 | Test 0
	{
		Qname: "svc1.ns1.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("svc1.ns1.example.com.   60  IN  A   192.0.1.1"),
		},
	},
	// Existing Ingress | Test 1
	{
		Qname: "domain.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("domain.example.com. 60  IN  A   192.0.0.1"),
		},
	},
	// Ingress takes precedence over services | Test 2
	{
		Qname: "svc2.ns1.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("svc2.ns1.example.com.   60  IN  A   192.0.0.2"),
		},
	},
	// Non-existing Service | Test 3
	{
		Qname: "svcX.ns1.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeNameError,
		Ns: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
	// Non-existing Ingress | Test 4
	{
		Qname: "d0main.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeNameError,
		Ns: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
	// SOA for the existing domain | Test 5
	{
		Qname: "domain.example.com.", Qtype: dns.TypeSOA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
	// Service with no public addresses | Test 6
	{
		Qname: "svc3.ns1.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeNameError,
		Ns: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
	// Real service, wrong query type | Test 7
	{
		Qname: "svc3.ns1.example.com.", Qtype: dns.TypeCNAME, Rcode: dns.RcodeSuccess,
		Ns: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
	// Ingress FQDN == zone | Test 8
	{
		Qname: "example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("example.com.    60  IN  A   192.0.0.3"),
		},
	},
	// Existing Ingress with a mix of lower and upper case letters | Test 9
	{
		Qname: "dOmAiN.eXamPLe.cOm.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("domain.example.com. 60  IN  A   192.0.0.1"),
		},
	},
	// Existing Service with a mix of lower and upper case letters | Test 10
	{
		Qname: "svC1.Ns1.exAmplE.Com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("svc1.ns1.example.com.   60  IN  A   192.0.1.1"),
		},
	},
	// Existing Service A record, but no AAAA record | Test 11
	{
		Qname: "svc2.ns1.example.com.", Qtype: dns.TypeAAAA, Rcode: dns.RcodeSuccess,
		Ns: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
	// Existing Service AAAA record only | Test 12
	{
		Qname: "svc4.ns1.example.com.", Qtype: dns.TypeAAAA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.AAAA("svc4.ns1.example.com.    60  IN  AAAA    fd12:3456:789a:2::"),
		},
	},
	// Existing Service AAAA-only record, A query returns NODATA (RFC 4074) | Test 13
	{
		Qname: "svc4.ns1.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Ns: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
	// Existing Service IPv6 | Test 16
	{
		Qname: "svc1.ns1.example.com.", Qtype: dns.TypeAAAA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.AAAA("svc1.ns1.example.com.    60  IN  AAAA    fd12:3456:789a:1::"),
		},
	},
	// lookup apex NS record | Test 17
	{
		Qname: "example.com.", Qtype: dns.TypeNS, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.NS("example.com.   60  IN  NS  dns1.kube-system.example.com"),
		},
		Extra: []dns.RR{
			test.A("dns1.kube-system.example.com.   60  IN  A   192.0.1.53"),
		},
	},
	// Lookup that relies on a wildcard | Test 18
	{
		Qname: "not-explicitly-defined-label.wildcard.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("not-explicitly-defined-label.wildcard.example.com. 60  IN  A   192.0.0.6"),
		},
	},
	// Lookup with a matching wildcard but a more specific entry | Test 19
	{
		Qname: "specific-subdomain.wildcard.example.com.", Qtype: dns.TypeA, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("specific-subdomain.wildcard.example.com. 60  IN  A   192.0.0.7"),
		},
	},
	// Existing Endpoint | TXT record
	{
		Qname: "endpoint.example.com.", Qtype: dns.TypeTXT, Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.TXT("endpoint.example.com. 60  IN  TXT   \"Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor i\" \"n reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum.\""),
		},
	},
	// Non-existing Endpoint | TXT record
	{
		Qname: "endpointX.ns1.example.com.", Qtype: dns.TypeTXT, Rcode: dns.RcodeNameError,
		Ns: []dns.RR{
			test.SOA("example.com.  60  IN  SOA dns1.kube-system.example.com. hostmaster.example.com. 1499347823 7200 1800 86400 5"),
		},
	},
}

var testsFallthrough = []FallthroughCase{
	// Match found, fallthrough enabled | Test 0
	{
		Case:             test.Case{Qname: "example.com.", Qtype: dns.TypeA},
		FallthroughZones: []string{"."}, FallthroughExpected: false,
	},
	// No match found, fallthrough enabled | Test 1
	{
		Case:             test.Case{Qname: "non-existent.example.com.", Qtype: dns.TypeA},
		FallthroughZones: []string{"."}, FallthroughExpected: true,
	},
	// Match found, fallthrough for different zone | Test 2
	{
		Case:             test.Case{Qname: "example.com.", Qtype: dns.TypeA},
		FallthroughZones: []string{"not-example.com."}, FallthroughExpected: false,
	},
	// No match found, fallthrough for different zone | Test 3
	{
		Case:             test.Case{Qname: "non-existent.example.com.", Qtype: dns.TypeA},
		FallthroughZones: []string{"not-example.com."}, FallthroughExpected: false,
	},
	// No fallthrough on gw apex | Test 4
	{
		Case:             test.Case{Qname: "dns1.kube-system.example.com.", Qtype: dns.TypeA},
		FallthroughZones: []string{"."}, FallthroughExpected: false,
	},
}

var testServiceIndexes = map[string][]netip.Addr{
	"svc1.ns1":         {netip.MustParseAddr("192.0.1.1"), netip.MustParseAddr("fd12:3456:789a:1::")},
	"svc2.ns1":         {netip.MustParseAddr("192.0.1.2")},
	"svc3.ns1":         {},
	"svc4.ns1":         {netip.MustParseAddr("fd12:3456:789a:2::")},
	"dns1.kube-system": {netip.MustParseAddr("192.0.1.53")},
}

func testServiceLookup(keys []string) (results []netip.Addr, raws []string) {
	for _, key := range keys {
		results = append(results, testServiceIndexes[strings.ToLower(key)]...)
	}
	return results, raws
}

var testIngressIndexes = map[string][]netip.Addr{
	"domain.example.com":                      {netip.MustParseAddr("192.0.0.1")},
	"svc2.ns1.example.com":                    {netip.MustParseAddr("192.0.0.2")},
	"example.com":                             {netip.MustParseAddr("192.0.0.3")},
	"shadow.example.com":                      {netip.MustParseAddr("192.0.0.4")},
	"shadow-vs.example.com":                   {netip.MustParseAddr("192.0.0.5")},
	"*.wildcard.example.com":                  {netip.MustParseAddr("192.0.0.6")},
	"specific-subdomain.wildcard.example.com": {netip.MustParseAddr("192.0.0.7")},
}

func testIngressLookup(keys []string) (results []netip.Addr, raws []string) {
	for _, key := range keys {
		results = append(results, testIngressIndexes[strings.ToLower(key)]...)
	}
	return results, raws
}

var testDNSEndpointIndexes = map[string][]netip.Addr{
	"domain.endpoint.example.com": {netip.MustParseAddr("192.0.4.1")},
	"endpoint.example.com":        {netip.MustParseAddr("192.0.4.4")},
}

// test implementation for TXT multiple records does not work correctly
// because it is confused with the concatenation of strings longer than 255 bytes
// The loop in https://github.com/coredns/coredns/blob/master/plugin/test/helpers.go#L209
// may be the origin of the problem
var testDNSEndpointTxtIndexes = map[string][]string{
	"endpoint.example.com":        {"Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum."},
}

func testDNSEndpointLookup(keys []string) (results []netip.Addr, raws []string) {
	for _, key := range keys {
		results = append(results, testDNSEndpointIndexes[strings.ToLower(key)]...)
	}
	for _, key := range keys {
		raws = append(raws, testDNSEndpointTxtIndexes[strings.ToLower(key)]...)
	}
	return results, raws
}

func setupLookupFuncs(gw *Gateway) {
	if resource := gw.lookupResource("Ingress"); resource != nil {
		resource.lookup = testIngressLookup
	}
	if resource := gw.lookupResource("Service"); resource != nil {
		resource.lookup = testServiceLookup
	}
	if resource := gw.lookupResource("DNSEndpoint"); resource != nil {
		resource.lookup = testDNSEndpointLookup
	}
}

// TestUpdateResourcesIsolation verifies that updateResources does not share
// resourceWithIndex pointers with the package-level staticResources slice.
// Mutating a lookup func on gw.Resources must not affect staticResources.
func TestUpdateResourcesIsolation(t *testing.T) {
	gw := newGateway()
	gw.updateResources([]string{"Ingress", "Service", "Node"})

	sentinel := func([]string) ([]netip.Addr, []string) { return nil, nil }

	for _, r := range gw.Resources {
		r.lookup = sentinel
	}

	for _, sr := range staticResources {
		// The lookup field of each staticResources entry must still be noop,
		// not the sentinel we assigned to gw.Resources entries.
		addrs, _ := sr.lookup(nil)
		if len(addrs) != 0 {
			t.Errorf("staticResources entry %q was mutated by updateResources", sr.name)
		}
		// A direct pointer comparison: if sr is the same struct as the one
		// inside gw.Resources the test above may not be enough, so also
		// confirm the pointer itself differs from every gw.Resources entry.
		for _, gr := range gw.Resources {
			if sr == gr {
				t.Errorf("gw.Resources entry %q shares a pointer with staticResources", sr.name)
			}
		}
	}
}

// TestUpdateResourcesUnknown verifies that unknown resource names are silently
// skipped and do not end up in gw.Resources.
func TestUpdateResourcesUnknown(t *testing.T) {
	gw := newGateway()
	gw.updateResources([]string{"Ingress", "UnknownThing"})

	if len(gw.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(gw.Resources))
	}
	if gw.Resources[0].name != "Ingress" {
		t.Errorf("expected Ingress, got %s", gw.Resources[0].name)
	}
}

// TestAnyIngressForHostname verifies that anyIngressForHostname returns true
// when an ingress exists for the hostname (regardless of class) and false
// when no ingress exists.
func TestAnyIngressForHostname(t *testing.T) {
	gw := newGateway()
	gw.Zones = []string{"whiteblossom.net."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, nil)
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = &KubeController{hasSynced: true}

	// Create a cloudflare-tunnel ingress for code-dev.whiteblossom.net
	ingressClass := "cloudflare-tunnel"
	ingress := &networking.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "code-dev-tunnel",
			Namespace: "default",
		},
		Spec: networking.IngressSpec{
			IngressClassName: &ingressClass,
			Rules: []networking.IngressRule{
				{
					Host: "code-dev.whiteblossom.net",
				},
			},
		},
	}

	// Create an indexer and add the ingress to it
	ingressIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{ingressHostnameIndex: ingressHostnameIndexFunc})
	if err := ingressIndexer.Add(ingress); err != nil {
		t.Fatalf("failed to add ingress to indexer: %v", err)
	}

	// Store the indexer on the Gateway's Ingress resource
	resource := gw.lookupResource("Ingress")
	if resource == nil {
		t.Fatal("Ingress resource not found in Gateway")
	}
	resource.controller = ingressIndexer

	// Test: hostname with existing ingress should return true
	if !gw.anyIngressForHostname("code-dev.whiteblossom.net") {
		t.Error("expected anyIngressForHostname to return true for code-dev.whiteblossom.net")
	}

	// Test: hostname without ingress should return false
	if gw.anyIngressForHostname("nonexistent.whiteblossom.net") {
		t.Error("expected anyIngressForHostname to return false for nonexistent.whiteblossom.net")
	}
}

// TestServeDNSCloudflareTunnelFallthrough verifies that when a hostname has
// an ingress with a non-matching class (e.g. cloudflare-tunnel), the
// ingressClusterIPFallback does NOT inject Traefik ClusterIP, allowing
// the query to fall through to the next CoreDNS plugin.
func TestServeDNSCloudflareTunnelFallthrough(t *testing.T) {
	ctrl := &KubeController{hasSynced: true}
	gw := newGateway()
	gw.Zones = []string{"whiteblossom.net."}
	gw.Next = test.NextHandler(dns.RcodeSuccess, Fallen{})
	gw.ExternalAddrFunc = gw.SelfAddress
	gw.Controller = ctrl
	gw.ingressClusterIPFallback = true
	gw.resourceFilters.ingressClasses = []string{"traefik"}
	gw.Fall = fall.F{Zones: []string{"."}}

	// Create a cloudflare-tunnel ingress for code-dev.whiteblossom.net
	ingressClass := "cloudflare-tunnel"
	ingress := &networking.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "code-dev-tunnel",
			Namespace: "default",
		},
		Spec: networking.IngressSpec{
			IngressClassName: &ingressClass,
			Rules: []networking.IngressRule{
				{
					Host: "code-dev.whiteblossom.net",
				},
			},
		},
	}

	// Create an indexer and add the ingress to it
	ingressIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{ingressHostnameIndex: ingressHostnameIndexFunc})
	if err := ingressIndexer.Add(ingress); err != nil {
		t.Fatalf("failed to add ingress to indexer: %v", err)
	}

	// Store the indexer on the Gateway's Ingress resource
	resource := gw.lookupResource("Ingress")
	if resource == nil {
		t.Fatal("Ingress resource not found in Gateway")
	}
	resource.controller = ingressIndexer

	// Query from a private IP (simulating cluster-internal client)
	ctx := context.TODO()
	r := new(dns.Msg)
	r.SetQuestion("code-dev.whiteblossom.net.", dns.TypeA)
	w := dnstest.NewRecorder(&test.ResponseWriter{
		RemoteIP: "10.0.0.1",
	})

	_, err := gw.ServeDNS(ctx, w, r)

	// Should fall through (return Fallen error) because cloudflare-tunnel
	// ingress exists but doesn't match traefik class
	if !errors.As(err, &Fallen{}) {
		t.Errorf("expected fall-through for cloudflare-tunnel domain, got err=%v, rcode=%d", err, w.Msg.Rcode)
	}
}

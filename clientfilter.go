package gateway

import (
	"net"
	"net/netip"

	"github.com/miekg/dns"
)

// nodeSubnetLookupFunc returns the *net.IPNet of the node interface that
// owns the given candidate address, or nil if no node's interface carries
// that exact IP (e.g. kube-vip / service VIPs). The lookup is backed by the
// Node informer cache populated from the
// "k8s-gateway.whiteblossom.net/interfaces" annotation; see buildNodeInterfaceLookup.
type nodeSubnetLookupFunc func(netip.Addr) *net.IPNet

// filterAddressesByClientSubnet narrows addrs to those whose own node
// interface subnet contains the ECS client IP.
//
// Algorithm:
//   - If addrs is empty or the request carries no usable ECS option, return
//     addrs unchanged (fail-open).
//   - For each candidate address, ask nodeSubnetLookup for the subnet of
//     the node interface that owns that candidate.
//   - If the lookup returns nil (candidate is a VIP, not on any node's
//     interface), keep it (fail-open).
//   - Otherwise keep the candidate only if the ECS client IP falls within
//     the returned subnet.
//
// The ECS SourceNetmask is deliberately ignored: with Blocky configured to
// forward ipv4Mask:32 the full client IP is carried as the ECS address, and
// the filter's job is to compare that client IP against the *real* interface
// subnet of the node hosting each candidate.
func filterAddressesByClientSubnet(req *dns.Msg, addrs []netip.Addr, lookup nodeSubnetLookupFunc, mode string) []netip.Addr {
	if len(addrs) == 0 {
		return addrs
	}
	clientIP := extractClientIP(req)
	if !clientIP.IsValid() {
		// No ECS option (or malformed) → fail-open.
		return addrs
	}
	kept := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if lookup == nil {
			// No node interface data available → fail-open.
			kept = append(kept, addr)
			continue
		}
		subnet := lookup(addr)
		if subnet == nil {
			// Candidate is a VIP / not on any node interface.
			if mode == "strict" {
				continue // drop
			}
			kept = append(kept, addr)
			continue
		}
		if subnetContainsClient(subnet, clientIP) {
			kept = append(kept, addr)
		}
	}
	return kept
}

// extractClientIP reads the EDNS0_SUBNET option from the request and returns
// its Address as a netip.Addr. The SourceNetmask is intentionally ignored —
// only the address is used by the filter. Returns an invalid netip.Addr if
// the request is nil, has no EDNS0 OPT record, has no ECS option, or the
// ECS address is unusable (so callers fail open).
func extractClientIP(req *dns.Msg) netip.Addr {
	if req == nil {
		return netip.Addr{}
	}
	opt := req.IsEdns0()
	if opt == nil {
		return netip.Addr{}
	}
	for _, o := range opt.Option {
		subnetOpt, ok := o.(*dns.EDNS0_SUBNET)
		if !ok {
			continue
		}
		// Unknown address family is malformed → fail-open.
		if subnetOpt.Family != 1 && subnetOpt.Family != 2 {
			return netip.Addr{}
		}
		// Zero address is malformed → fail-open.
		if len(subnetOpt.Address) == 0 {
			return netip.Addr{}
		}
		ip, ok := netip.AddrFromSlice(subnetOpt.Address)
		if !ok {
			return netip.Addr{}
		}
		// Unmap strips a 4-in-6 representation so that v4 client IPs
		// compare correctly against v4 interface subnets.
		return ip.Unmap()
	}
	return netip.Addr{}
}

// subnetContainsClient reports whether the client IP falls within the given
// subnet, accounting for netip.Addr → net.IP conversion pitfalls
// (4-in-6 byte representations).
func subnetContainsClient(subnet *net.IPNet, clientIP netip.Addr) bool {
	if subnet == nil {
		return false
	}
	ip := net.IP(clientIP.AsSlice())
	if ip == nil {
		return false
	}
	// Match address families: if the subnet is IPv4, use the 4-byte form of
	// the client IP; an IPv4 client cannot belong to an IPv6 prefix anyway.
	if len(subnet.IP) == net.IPv4len {
		ip = ip.To4()
		if ip == nil {
			return false
		}
	}
	return subnet.Contains(ip)
}

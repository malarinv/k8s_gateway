package gateway

import (
	"net"
	"net/netip"

	"github.com/miekg/dns"
)

// filterAddressesByClientSubnet returns the subset of addrs that fall within
// the EDNS0 Client Subnet (ECS) option carried by the request, if any.
//
// When clientFiltering is disabled, or the request carries no ECS option, or
// the ECS option is malformed, addrs is returned unchanged (fail-open).
// When a valid ECS option is present, only addresses whose IP is contained in
// the ECS network (address/sourceNetmask) are kept. If filtering removes every
// address, the caller should treat the result as "no match" and fall through
// to the next plugin — that fall-through is the caller's responsibility
// (it reuses the existing gw.Fall path).
func filterAddressesByClientSubnet(req *dns.Msg, addrs []netip.Addr) []netip.Addr {
	if len(addrs) == 0 {
		return addrs
	}
	subnet := extractClientSubnet(req)
	if subnet == nil {
		// No ECS option (or malformed) → fail open: return unfiltered.
		return addrs
	}
	kept := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if subnetContainsAddr(*subnet, addr) {
			kept = append(kept, addr)
		}
	}
	return kept
}

// extractClientSubnet reads the EDNS0_SUBNET option from the request and
// returns it as a parsed *net.IPNet. Returns nil if no ECS option is present
// or if the option is malformed (so callers fail open).
func extractClientSubnet(req *dns.Msg) *net.IPNet {
	if req == nil {
		return nil
	}
	opt := req.IsEdns0()
	if opt == nil {
		return nil
	}
	for _, o := range opt.Option {
		if subnetOpt, ok := o.(*dns.EDNS0_SUBNET); ok {
			return edns0SubnetToIPNet(subnetOpt)
		}
	}
	return nil
}

// edns0SubnetToIPNet converts a miekg/dns EDNS0_SUBNET option to a *net.IPNet.
// Returns nil if the option is malformed (zero netmask, nil address, or
// unrecognized address family).
func edns0SubnetToIPNet(o *dns.EDNS0_SUBNET) *net.IPNet {
	if o == nil || o.Address == nil || o.SourceNetmask == 0 {
		return nil
	}
	var bits int
	switch o.Family {
	case 1: // IPv4
		bits = 32
	case 2: // IPv6
		bits = 128
	default:
		return nil
	}
	if int(o.SourceNetmask) > bits {
		return nil
	}
	// Normalize Address to its canonical 4- or 16-byte form based on family,
	// so net.IPNet.Contains() matches correctly (ParseIP can return 16-byte
	// IPv4-in-IPv6 representations; net.IPNet matches on raw byte length).
	addr := o.Address
	if o.Family == 1 {
		addr = addr.To4()
	} else {
		addr = addr.To16()
	}
	if addr == nil {
		return nil
	}
	mask := net.CIDRMask(int(o.SourceNetmask), bits)
	return &net.IPNet{IP: addr.Mask(mask), Mask: mask}
}

// subnetContainsAddr reports whether addr falls within the IPNet, accounting
// for netip.Addr → net.IP conversion and IPv4-in-IPv6 pitfalls.
func subnetContainsAddr(n net.IPNet, addr netip.Addr) bool {
	var ip net.IP
	if addr.Is4() {
		ip = net.IP(addr.AsSlice())
		// Match against the subnet's family: if the subnet is IPv4, use 4-byte
		// form; if IPv6, the 4-in-6 cannot be in a /v6 prefix anyway.
		if len(n.IP) == net.IPv4len {
			ip = ip.To4()
		}
	} else {
		ip = net.IP(addr.AsSlice())
	}
	if ip == nil {
		return false
	}
	return n.Contains(ip)
}

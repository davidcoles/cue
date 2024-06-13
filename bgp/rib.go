/*
 * VC5 load balancer. Copyright (C) 2021-present David Coles
 *
 * This program is free software; you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation; either version 2 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License along
 * with this program; if not, write to the Free Software Foundation, Inc.,
 * 51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.
 */

package bgp

import (
	//"net"
	"net/netip"
)

type _update struct {
	RIB        []netip.Addr
	Parameters Parameters
}

type _rib []netip.Addr

func (r _rib) dup() (ret []netip.Addr) {
	for _, i := range r {
		ret = append(ret, i)
	}
	return
}

func newupdate(p Parameters, r []netip.Addr) _update {
	//var rib []netip.Addr // create a seperate copy of the slice
	//for _, i := range r {
	//	rib = append(rib, i)
	//}
	return _update{RIB: _rib(r).dup(), Parameters: p}
}

func (u *_update) adjRIBOut(ipv6 bool) (out []netip.Addr) {
	//return u.filter(ipv6)
	return u.Parameters.filter(ipv6, u.RIB)
}

//func (u *_update) initial(ipv6 bool) map[netip.Addr]bool {
//	out := map[netip.Addr]bool{}
//	for _, i := range u.Filter(ipv6) {
//		out[i] = true
//	}
//	return out
//}

//func (u *_update) filter(ipv6 bool) []netip.Addr {
//	return u.Parameters.filter(ipv6, u.RIB)
//}

func (p *Parameters) filter(ipv6 bool, dest []netip.Addr) (pass []netip.Addr) {

	// ipv6 should be set to true iff the bearer TCP connection is
	// establshed over IPv6

	// If the Multiprotocol flag is not set then address of a
	// different type to that of the connection will be filtered out.

filter:
	for _, i := range dest {

		if !p.Multiprotocol {

			if i.Is6() && !ipv6 {
				continue
			}

			if i.Is4() && ipv6 {
				continue
			}
		}

		ip := i

		for _, ipnet := range p.Accept {
			n := ipnet
			if n.Contains(ip) {
				pass = append(pass, i)
				continue filter
			}
		}

		for _, ipnet := range p.Reject {
			n := ipnet
			if n.Contains(ip) {
				continue filter
			}
		}

		pass = append(pass, i)
	}

	return pass
}

//func (u *_update) xSource() net.IP {
//	return net.ParseIP(ip_string(u.Parameters.SourceIP))
//}

//func (u *_update) nlri(old []netip.Addr, ipv6, force bool) ([]netip.Addr, map[netip.Addr]bool) {
//	return _nlri(u.adjRIBOut(ipv6), old, force)
//}

//func _nlri(curr, prev []netip.Addr, force bool) (list []netip.Addr, nlri map[netip.Addr]bool) {
func (u *_update) nlri(prev []netip.Addr, ipv6, force bool) ([]netip.Addr, map[netip.Addr]bool) {
	curr := u.adjRIBOut(ipv6)
	var list []netip.Addr

	nlri := map[netip.Addr]bool{}
	new := map[netip.Addr]bool{}
	old := map[netip.Addr]bool{}

	for _, i := range curr {
		new[i] = true
	}

	for _, i := range prev {
		old[i] = true
	}

	// if IP was in the previous list but not in the new list then withdraw
	for i, _ := range old {
		if _, ok := new[i]; !ok {
			nlri[i] = false
		}
	}

	// if force readvertise or IP is in the current list but not in the old one then advertise - add to new list anyway
	for i, _ := range new {
		list = append(list, i)
		if _, ok := old[i]; !ok || force {
			nlri[i] = true
		}
	}

	return list, nlri
}

func (c *_update) updates(p _update, ipv6 bool) (uint64, uint64, map[netip.Addr]bool) {
	nrli := map[netip.Addr]bool{}

	var advertise uint64
	var withdraw uint64

	var vary bool = c.Parameters.Diff(p.Parameters)

	curr := map[netip.Addr]bool{}
	prev := map[netip.Addr]bool{}

	for _, ip := range c.adjRIBOut(ipv6) {
		curr[ip] = true
	}

	for _, ip := range p.adjRIBOut(ipv6) {
		prev[ip] = true
	}

	for ip, _ := range curr {
		_, ok := prev[ip] // if didn't exist in previous rib, or params have changed then need to advertise
		if !ok || vary {
			advertise++
			nrli[ip] = true
		}
	}

	for ip, _ := range prev {
		_, ok := curr[ip] // if not in current rib then need to withdraw
		if !ok {
			withdraw++
			nrli[ip] = false
		}
	}

	return advertise, withdraw, nrli
}

/*
func Filter(dest []IP, filter []IP) []IP {

	reject := map[IP]bool{}

	if len(filter) == 0 {
		return dest
	}

	for _, i := range filter {
		reject[i] = true
	}

	var o []IP

	for _, i := range dest {
		if _, rejected := reject[i]; !rejected {
			o = append(o, i)
		}
	}

	return o
}
*/

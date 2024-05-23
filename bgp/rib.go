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
	"net"
	"net/netip"
)

func newupdate(p Parameters, r []IP) Update {
	var rib []netip.Addr
	for _, i := range r {
		rib = append(rib, netip.AddrFrom4(i))
	}
	return Update{RIB2: rib, Parameters: p}
}

func newupdate2(p Parameters, r []netip.Addr) Update {
	var rib []netip.Addr
	for _, i := range r {
		rib = append(rib, i)
	}
	return Update{RIB2: rib, Parameters: p}
}

type Update struct {
	RIB        []IP
	RIB2       []netip.Addr
	Parameters Parameters
}

func (r *Update) adjRIBOutString(ipv6 bool) (out []string) {
	for _, p := range r.Filter(ipv6) {
		//out = append(out, ip_string(p))
		out = append(out, p.String())
	}
	return
}

func (r *Update) adjRIBOut(ipv6 bool) (out []netip.Addr) {
	return r.Filter(ipv6)
}

func (u *Update) Initial(ipv6 bool) map[netip.Addr]bool {
	out := map[netip.Addr]bool{}
	for _, i := range u.Filter(ipv6) {
		out[i] = true
	}
	return out
}

//func (r *Update) adjRIBOutP() ([]IP, Parameters) {
func (r *Update) adjRIBOutP(ipv6 bool) ([]netip.Addr, Parameters) {
	return r.Filter(ipv6), r.Parameters
}

func (u *Update) Filter(ipv6 bool) []netip.Addr {
	return u.Parameters.Filter(ipv6, u.RIB2)
}

func (p *Parameters) Filter(ipv6 bool, dest []netip.Addr) (pass []netip.Addr) {

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

		//ip := net.ParseIP(ip_string(i))
		//ip := net.ParseIP(i.String())
		ip := i

		//if ip == nil {
		//		continue
		//	}

		for _, ipnet := range p.Accept {
			//n := net.IPNet(ipnet)
			n := ipnet
			if n.Contains(ip) {
				pass = append(pass, i)
				continue filter
			}
		}

		for _, ipnet := range p.Reject {
			//n := net.IPNet(ipnet)
			n := ipnet
			if n.Contains(ip) {
				continue filter
			}
		}

		pass = append(pass, i)
	}

	return pass
}

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

func advertise(r []IP) map[IP]bool {
	n := map[IP]bool{}
	for _, ip := range r {
		n[ip] = true
	}
	return n
}

func to_string(in []IP) (out []string) {
	for _, p := range in {
		out = append(out, ip_string(p))
	}
	return
}

func (r Update) Copy() Update {
	var rib []IP

	for _, x := range r.RIB {
		rib = append(rib, x)
	}

	return Update{RIB: rib, Parameters: r.Parameters}
}

func (u *Update) Source() net.IP {
	return net.ParseIP(ip_string(u.Parameters.SourceIP))
}

func NLRI(curr, prev []netip.Addr, force bool) ([]netip.Addr, map[netip.Addr]bool) {
	out := map[netip.Addr]bool{}

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
			out[i] = false
		}
	}

	// if force readvertise or IP is in the current list but not in the old one then advertise
	for i, _ := range new {
		if _, ok := old[i]; !ok || force {
			out[i] = true
		}
	}

	return curr, out
}

func (c *Update) updates(p Update, ipv6 bool) (uint64, uint64, map[netip.Addr]bool) {
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
func (c *Update) oldupdates(p Update) (uint64, uint64, map[IP]bool) {
	nrli := map[IP]bool{}

	var advertise uint64
	var withdraw uint64

	var vary bool = c.Parameters.Diff(p.Parameters)

	curr := map[IP]bool{}
	prev := map[IP]bool{}

	for _, ip := range c.adjRIBOut() {
		curr[ip] = true
	}

	for _, ip := range p.adjRIBOut() {
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
*/

/*
func RIBSDiffer(a, b []IP) bool {

	x := map[IP]bool{}
	for _, i := range a {
		x[i] = true
	}

	y := map[IP]bool{}
	for _, i := range b {
		y[i] = true
	}

	if len(y) != len(y) {
		return true
	}

	for i, _ := range x {
		_, ok := y[i]
		if !ok {
			return true
		}
		delete(y, i)
	}

	return len(y) != 0
}

*/

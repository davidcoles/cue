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
	"net/netip"
)

type message interface {
	Type() uint8
	Body() []byte
}

type keepalive struct{}

type notification struct {
	code uint8
	sub  uint8
	data []byte
}

func (n notification) message() []byte {
	return append([]byte{n.code, n.sub}, n.data[:]...)
}

func (n *notification) Type() uint8  { return M_NOTIFICATION }
func (n *notification) Body() []byte { return n.message() }

func (o *open) Type() uint8  { return M_OPEN }
func (o *open) Body() []byte { return o.message() }

func (k *keepalive) Type() uint8  { return M_KEEPALIVE }
func (k *keepalive) Body() []byte { return nil }

type update []byte

func (f *update) Type() uint8  { return M_UPDATE }
func (f *update) Body() []byte { return (*f)[:] }

type other struct {
	mtype uint8
	body  []byte
}

func (o *other) Type() uint8  { return o.mtype }
func (o *other) Body() []byte { return o.body }

func (n *notification) parse(d []byte) bool {
	if len(d) < 2 {
		return false
	}
	n.code = d[0]
	n.sub = d[1]
	n.data = d[2:] // if len(d) is 2 then this will return an empty slice, not a panic
	return true
}

type open struct {
	asNumber      uint16
	holdTime      uint16
	routerID      [4]byte
	multiprotocol bool

	version byte
	op      []byte
}

func (o *open) parse(d []byte) bool {
	if len(d) < 10 {
		return false
	}
	o.version = d[0]
	o.asNumber = (uint16(d[1]) << 8) | uint16(d[2])
	o.holdTime = (uint16(d[3]) << 8) | uint16(d[4])
	copy(o.routerID[:], d[5:9])
	o.op = d[10:]
	return true
}

func (o *open) message() []byte {
	as := htons(o.asNumber)
	ht := htons(o.holdTime)
	id := o.routerID

	open := []byte{4, as[0], as[1], ht[0], ht[1], id[0], id[1], id[2], id[3]}
	var params []byte

	// AFI[2], Reserved[1](always 0), SAFI[1]

	// https://infocenter.nokia.com/public/7750SR222R1A/index.jsp?topic=%2Fcom.nokia.Unicast_Guide%2Fmulti-protocol_-ai9exj5yje.html
	// https://datatracker.ietf.org/doc/html/rfc3392 - Capabilities Advertisement with BGP-4
	// Capability Code (1 octet), Capability Length (1 octet), Capability Value (variable)
	mp_ipv4 := []byte{BGP4_MP, 4, 0, 1, 0, 1} // Capability Code (1 octet), Length (1 octet), Value [IPv4 unicast AFI 1, SAFI 1]
	mp_ipv6 := []byte{BGP4_MP, 4, 0, 2, 0, 1} // Capability Code (1 octet), Length (1 octet), Value [IPv6 unicast AFI 2, SAFI 1]

	// Optional Parameters: Parm.Type[1], Parm.Length[1], Parm.Value[...]
	param_ipv4 := append([]byte{CAPABILITIES_OPTIONAL_PARAMETER, byte(len(mp_ipv4))}, mp_ipv4...)
	param_ipv6 := append([]byte{CAPABILITIES_OPTIONAL_PARAMETER, byte(len(mp_ipv6))}, mp_ipv6...)

	if o.multiprotocol {
		params = append(params, param_ipv6...)
		params = append(params, param_ipv4...)
	}

	params = append([]byte{byte(len(params))}, params...)

	return append(open, params...)
}

type advert struct {
	NextHop       [4]byte
	NextHop6      [16]byte
	ASNumber      uint16
	LocalPref     uint32
	MED           uint32
	Communities   []Community
	RIB           map[netip.Addr]bool
	Multiprotocol bool
	IPv6          bool

	external bool
}

func (a *advert) withParameters(p Parameters, remoteASNumber uint16) (r advert) {
	r = *a
	r.Communities = p.Communities
	r.LocalPref = p.LocalPref
	r.MED = p.MED
	r.external = a.ASNumber != remoteASNumber
	return
}

func (a *advert) updates(m map[netip.Addr]bool) (ret []message) {

	if len(m) < 1 {
		return nil
	}

	msg := a.message(m)

	if len(msg) < 4000 {
		return append(ret, &msg)
	}

	if len(m) == 1 {
		// couldn't fit a singe prefix into one UPDATE message extremely
		// suspect - maybe the communities list is ridiculously long
		return nil
	}

	// split the set of prefixes in half and try each recursively -
	// indicates a fairly pathological usage of the library!
	l := len(m) / 2

	m1 := map[netip.Addr]bool{}
	m2 := map[netip.Addr]bool{}

	var n int
	for k, v := range m {
		if n < l {
			m1[k] = v
		} else {
			m2[k] = v
		}
		n++
	}

	if m := a.updates(m1); len(m) < 1 {
		return nil
	} else {
		ret = append(ret, m...)
	}

	if m := a.updates(m2); len(m) < 1 {
		return nil
	} else {
		ret = append(ret, m...)
	}

	return ret
}

//func (u *update) message(rib map[netip.Addr]bool) []byte {
func (a *advert) message(rib map[netip.Addr]bool) update {

	next_hop_address6 := a.NextHop6[:] // should be 16 or 32 bytes - a global adddress or global+link-local pair
	next_hop_address4 := a.NextHop

	var withdrawn []byte
	var advertise []byte

	var withdrawn6 []byte
	var advertise6 []byte

	for k, v := range rib {
		if k.Is4() {
			i := k.As4()
			l := append([]byte{32}, i[:]...) // 32 bit prefix & 4 bytes

			if v {
				advertise = append(advertise, l...)
			} else {
				withdrawn = append(withdrawn, l...)
			}
		}

		if k.Is6() {
			i := k.As16()
			l := append([]byte{128}, i[:]...) // 128 bit prefix & 16 bytes

			if v {
				advertise6 = append(advertise6, l...)
			} else {
				withdrawn6 = append(withdrawn6, l...)
			}
		}
	}

	// <attribute type, attribute length, attribute value> [data ...]
	// (Well-known, Mandatory, Transitive, Complete, Regular length), 1(ORIGIN), 1(byte), 0(IGP)
	origin := []byte{WTCR, ORIGIN, 1, IGP}

	as_path := asPath(a.ASNumber, a.external) // Well-known, Mandatory

	// (Well-known, Mandatory, Transitive, Complete, Regular length). 2(AS_PATH), 0(bytes, if iBGP - may get updated)
	/*
		as_path := []byte{WTCR, AS_PATH, 0}

		// RFC 4271, 5.1.2:

		// When a BGP speaker originates a route then:
		//
		// a) the originating speaker includes its own AS number in a path
		//    segment, of type AS_SEQUENCE, in the AS_PATH attribute of all
		//    UPDATE messages sent to an external peer.  In this case, the AS
		//    number of the originating speaker's autonomous system will be
		//    the only entry the path segment, and this path segment will be
		//    the only segment in the AS_PATH attribute.
		//
		// b) the originating speaker includes an empty AS_PATH attribute in
		//    all UPDATE messages sent to internal peers.  (An empty AS_PATH
		//    attribute is one whose length field contains the value zero).

		if a.external {
			asn := htons(a.ASNumber)
			// Each AS path segment is represented by a triple <path segment type, path segment length, path segment value>
			as_sequence := []byte{AS_SEQUENCE, 1} // AS_SEQUENCE(2), 1 ASN
			as_sequence = append(as_sequence, asn[:]...)
			as_path = append(as_path, as_sequence...)
			as_path[2] = byte(len(as_sequence)) // update length field
		}
	*/

	// (Well-known, Mandatory, Transitive, Complete, Regular length), NEXT_HOP(3), 4(bytes)
	next_hop := append([]byte{WTCR, NEXT_HOP, 4}, next_hop_address4[:]...)

	path_attributes := []byte{}
	path_attributes = append(path_attributes, origin...)
	path_attributes = append(path_attributes, as_path...)
	path_attributes = append(path_attributes, next_hop...)

	// rfc4271: A BGP speaker MUST NOT include this attribute in UPDATE messages it sends to external peers ...
	// LOCAL_PREF is a well-known attribute that SHALL be included in
	// all UPDATE messages that a given BGP speaker sends to other
	// internal peers. (NB: SHALL is synonymous for MUST - an absolute requirement)
	if !a.external {
		local_pref := htonl(100)
		if a.LocalPref > 0 {
			local_pref = htonl(a.LocalPref) // default value
		}
		// (Well-known, Transitive, Complete, Regular length), LOCAL_PREF(5), 4 bytes
		attr := append([]byte{WTCR, LOCAL_PREF, 4}, local_pref[:]...)
		path_attributes = append(path_attributes, attr...)
	}

	if len(a.Communities) > 0 {
		communities := []byte{}
		for _, v := range a.Communities {
			c := htonl(uint32(v))
			communities = append(communities, c[:]...)
		}

		if len(communities) > 255 {
			hilo := htons(uint16(len(communities)))
			attr := append([]byte{OTCE, COMMUNITIES, hilo[0], hilo[1]}, communities...)
			path_attributes = append(path_attributes, attr...)
		} else {
			// (Optional, Transitive, Complete, Regular length), COMMUNITIES(8), n bytes
			attr := append([]byte{OTCR, COMMUNITIES, uint8(len(communities))}, communities...)
			path_attributes = append(path_attributes, attr...)
		}
	}

	if a.MED > 0 {
		// (Optional, Non-transitive, Complete, Regular length), MULTI_EXIT_DISC(4), 4 bytes
		med := htonl(a.MED)
		attr := append([]byte{ONCR, MULTI_EXIT_DISC, 4}, med[:]...)
		path_attributes = append(path_attributes, attr...)
	}

	if len(advertise6) > 0 {
		// https://datatracker.ietf.org/doc/html/rfc2545
		mp_reach_nlri := []byte{0, 2, 1} // IPv6 unicast AFI 2, SAFI 1
		mp_reach_nlri = append(mp_reach_nlri, byte(len(next_hop_address6)))
		mp_reach_nlri = append(mp_reach_nlri, next_hop_address6...)
		mp_reach_nlri = append(mp_reach_nlri, 0) // Number of SNPAs (1 octet) - none
		mp_reach_nlri = append(mp_reach_nlri, advertise6...)

		if len(mp_reach_nlri) > 255 {
			hilo := htons(uint16(len(mp_reach_nlri)))
			attr := append([]byte{ONCE, MP_REACH_NLRI, hilo[0], hilo[1]}, mp_reach_nlri...)
			path_attributes = append(path_attributes, attr...)
		} else {
			attr := append([]byte{ONCR, MP_REACH_NLRI, byte(len(mp_reach_nlri))}, mp_reach_nlri...)
			path_attributes = append(path_attributes, attr...)
		}
	}

	if len(withdrawn6) > 0 {
		mp_unreach_nlri := []byte{0, 2, 1} // IPv6 unicast AFI 2, SAFI 1
		mp_unreach_nlri = append(mp_unreach_nlri, withdrawn6...)

		if len(mp_unreach_nlri) > 255 {
			hilo := htons(uint16(len(mp_unreach_nlri)))
			attr := append([]byte{ONCE, MP_UNREACH_NLRI, hilo[0], hilo[1]}, mp_unreach_nlri...)
			path_attributes = append(path_attributes, attr...)
		} else {
			attr := append([]byte{ONCR, MP_UNREACH_NLRI, byte(len(mp_unreach_nlri))}, mp_unreach_nlri...)
			path_attributes = append(path_attributes, attr...)
		}
	}

	//   +-----------------------------------------------------+
	//   |   Withdrawn Routes Length (2 octets)                |
	//   +-----------------------------------------------------+
	//   |   Withdrawn Routes (variable)                       |
	//   +-----------------------------------------------------+
	//   |   Total Path Attribute Length (2 octets)            |
	//   +-----------------------------------------------------+
	//   |   Path Attributes (variable)                        |
	//   +-----------------------------------------------------+
	//   |   Network Layer Reachability Information (variable) |
	//   +-----------------------------------------------------+

	var update []byte

	wd := htons(uint16(len(withdrawn)))

	//update = append(update, htons(uint16(len(withdrawn)))...)
	update = append(update, wd[:]...)
	update = append(update, withdrawn...)

	if len(advertise) > 0 || len(advertise6) > 0 || len(withdrawn6) > 0 {
		pa := htons(uint16(len(path_attributes)))
		//update = append(update, htons(uint16(len(path_attributes)))...)
		update = append(update, pa[:]...)
		update = append(update, path_attributes...)
		update = append(update, advertise...)
	} else {
		update = append(update, 0, 0) // total path attribute length 0
	}

	return update
}

func asPath(asn uint16, external bool) (as_path []byte) {

	as_path = []byte{WTCR, AS_PATH, 0} // (Well-known, Mandatory, Transitive, Complete, Regular length)

	// RFC 4271, Section 5.1.2:

	// When a BGP speaker originates a route then:
	//
	// a) the originating speaker includes its own AS number in a path
	//    segment, of type AS_SEQUENCE, in the AS_PATH attribute of all
	//    UPDATE messages sent to an external peer.  In this case, the AS
	//    number of the originating speaker's autonomous system will be
	//    the only entry the path segment, and this path segment will be
	//    the only segment in the AS_PATH attribute.
	//
	// b) the originating speaker includes an empty AS_PATH attribute in
	//    all UPDATE messages sent to internal peers.  (An empty AS_PATH
	//    attribute is one whose length field contains the value zero).

	if external { // as per the above we only add a single AS_SEQUENCE path segment if eBGP - leave the as_path empty otherwise
		as_number := htons(asn)
		as_sequence := []byte{AS_SEQUENCE, 1} // Each AS path segment is represented by a triple <segment type, segment length, value>
		as_sequence = append(as_sequence, as_number[:]...)
		as_path = append(as_path, as_sequence...)
		as_path[2] = byte(len(as_sequence)) // update length field
	}

	return
}

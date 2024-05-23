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

func xxupdateMessage(ip IP, asn uint16, p Parameters, external bool, m map[IP]bool) message {
	return message{mtype: M_UPDATE, body: bgpupdate(p.SourceIP, p.ASNumber, external, p.LocalPref, p.MED, p.Communities, m)}
}

func updateMessage(hop IP, asn uint16, localpref uint32, med uint32, communities []Community, external bool, m map[IP]bool) message {
	return message{mtype: M_UPDATE, body: bgpupdate(hop, asn, external, localpref, med, communities, m)}
}

func openMessage(as uint16, ht uint16, id IP) message {
	return message{mtype: M_OPEN, open: open{version: 4, as: as, ht: ht, id: id}}
}

func keepaliveMessage() message {
	return message{mtype: M_KEEPALIVE}
}

func notificationMessage(code, sub uint8) message {
	return message{mtype: M_NOTIFICATION, notification: notification{code: code, sub: sub}}
}

func notificationM(code, sub uint8) notification {
	return notification{code: code, sub: sub}
}

func shutdownMessage(d string) message {
	return message{mtype: M_NOTIFICATION, notification: notification{
		code: CEASE, sub: ADMINISTRATIVE_SHUTDOWN, data: []byte(d),
	}}
}

func bgpupdate(hop IP, asn uint16, external bool, local_pref uint32, med uint32, communities []Community, status map[IP]bool) []byte {

	// Currently 256 avertisements results in a BGP UPDATE message of ~1307 octets
	// Maximum message size is 4096 octets so will need some way to split larger
	// avertisements down - maybe if the #prefixes is > 512 then split in two and
	// retry each half recurseively

	var withdrawn []byte
	var advertise []byte

	for k, v := range status {
		if v {
			advertise = append(advertise, 32, k[0], k[1], k[2], k[3]) // 32 bit prefix
		} else {
			withdrawn = append(withdrawn, 32, k[0], k[1], k[2], k[3]) // 32 bit prefix
		}
	}

	// <attribute type, attribute length, attribute value> [data ...]
	// (Well-known, Transitive, Complete, Regular length), 1(ORIGIN), 1(byte), 0(IGP)
	origin := []byte{WTCR, ORIGIN, 1, IGP}

	// (Well-known, Transitive, Complete, Regular length). 2(AS_PATH), 0(bytes, if iBGP - may get updated)
	as_path := []byte{WTCR, AS_PATH, 0}

	if external {
		// Each AS path segment is represented by a triple <path segment type, path segment length, path segment value>
		as_sequence := []byte{AS_SEQUENCE, 1} // AS_SEQUENCE(2), 1 ASN
		as_sequence = append(as_sequence, htons(asn)...)
		as_path = append(as_path, as_sequence...)
		as_path[2] = byte(len(as_sequence)) // update length field
	}

	// (Well-known, Transitive, Complete, Regular length), NEXT_HOP(3), 4(bytes)
	next_hop := append([]byte{WTCR, NEXT_HOP, 4}, hop[:]...)

	path_attributes := []byte{}
	path_attributes = append(path_attributes, origin...)
	path_attributes = append(path_attributes, as_path...)
	path_attributes = append(path_attributes, next_hop...)

	// rfc4271: A BGP speaker MUST NOT include this attribute in UPDATE messages it sends to external peers ...
	if !external {

		if local_pref == 0 {
			local_pref = 100
		}

		// (Well-known, Transitive, Complete, Regular length), LOCAL_PREF(5), 4 bytes
		attr := append([]byte{WTCR, LOCAL_PREF, 4}, htonl(local_pref)...)
		path_attributes = append(path_attributes, attr...)
	}

	if len(communities) > 0 {
		comms := []byte{}
		for k, v := range communities {
			if k < 60 { // should implement extended length
				c := htonl(uint32(v))
				comms = append(comms, c[:]...)
			}
		}

		// (Optional, Transitive, Complete, Regular length), COMMUNITIES(8), n bytes
		attr := append([]byte{OTCR, COMMUNITIES, uint8(len(comms))}, comms...)
		path_attributes = append(path_attributes, attr...)
	}

	if med > 0 {
		// (Optional, Non-transitive, Complete, Regular length), MULTI_EXIT_DISC(4), 4 bytes
		attr := append([]byte{ONCR, MULTI_EXIT_DISC, 4}, htonl(uint32(med))...)
		path_attributes = append(path_attributes, attr...)
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
	update = append(update, htons(uint16(len(withdrawn)))...)
	update = append(update, withdrawn...)

	if len(advertise) > 0 {
		update = append(update, htons(uint16(len(path_attributes)))...)
		update = append(update, path_attributes...)
		update = append(update, advertise...)
	} else {
		update = append(update, 0, 0) // total path attribute length 0 as there is no nlri
	}

	return update
}

type xopen struct {
	ASN      uint16
	HoldTime uint16
	ID       [4]byte
	MP       bool
}

func ntohl(a, b, c, d byte) uint32 {
	return uint32(a)<<24 | uint32(b)<<16 | uint32(c)<<8 | uint32(d)
}

func (o *xopen) message() []byte {
	as := htons(o.ASN)
	ht := htons(o.HoldTime)
	id := o.ID

	open := []byte{4, as[0], as[1], ht[0], ht[1], id[0], id[1], id[2], id[3]}

	// AFI[2], Reserved[1](always 0), SAFI[1]

	// https://infocenter.nokia.com/public/7750SR222R1A/index.jsp?topic=%2Fcom.nokia.Unicast_Guide%2Fmulti-protocol_-ai9exj5yje.html
	// https://datatracker.ietf.org/doc/html/rfc3392 - Capabilities Advertisement with BGP-4
	// Capability Code (1 octet), Capability Length (1 octet), Capability Value (variable)
	mp_ipv4 := []byte{BGP4_MP, 4, 0, 1, 0, 1} // Capability Code (1 octet), Length (1 octet), Value [IPv4 unicast AFI 1, SAFI 1]
	mp_ipv6 := []byte{BGP4_MP, 4, 0, 2, 0, 1} // Capability Code (1 octet), Length (1 octet), Value [IPv6 unicast AFI 2, SAFI 1]

	// Optional Parameters: Parm.Type[1], Parm.Length[1], Parm.Value[...]
	param_ipv4 := append([]byte{CAPABILITIES_OPTIONAL_PARAMETER, byte(len(mp_ipv4))}, mp_ipv4...)
	param_ipv6 := append([]byte{CAPABILITIES_OPTIONAL_PARAMETER, byte(len(mp_ipv6))}, mp_ipv6...)

	var params []byte
	if o.MP {
		params = append(params, param_ipv6...)
		params = append(params, param_ipv4...)
	}
	params = append([]byte{byte(len(params))}, params...)

	//return headerise(M_OPEN, append(open, params...))
	return append(open, params...)
}

func (o *xopen) render() []byte {
	return headerise(M_OPEN, o.message())
}

func keepalive() []byte {
	return headerise(M_KEEPALIVE, nil)
}

type update struct {
	NextHop       IP
	NextHop6      [16]byte
	ASNumber      uint16
	LocalPref     uint32
	MED           uint32
	Communities   []Community
	External      bool
	RIB           map[netip.Addr]bool
	Multiprotocol bool
	IPv6          bool
}

func (u *update) withParameters(p Parameters) (r update) {
	r = *u
	r.Communities = p.Communities
	r.LocalPref = p.LocalPref
	r.MED = p.MED
	return
}

func (u *update) render() (ret [][]byte) {

	for _, u := range u.messages(u.RIB) {
		ret = append(ret, headerise(M_UPDATE, u))
	}

	return ret
}

func (u *update) messages(m map[netip.Addr]bool) (ret [][]byte) {

	if len(m) < 1 {
		return nil
	}

	msg := u.message(m)

	if len(msg) < 4000 {
		return append(ret, msg)
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

	if m := u.messages(m1); len(m) < 1 {
		return nil
	} else {
		ret = append(ret, m...)
	}

	if m := u.messages(m2); len(m) < 1 {
		return nil
	} else {
		ret = append(ret, m...)
	}

	return ret
}

func (u *update) empty(rib map[netip.Addr]bool) bool {

	doipv4 := true
	doipv6 := true

	if !u.IPv6 && !u.Multiprotocol {
		//doipv6 = false
	}

	if u.IPv6 && !u.Multiprotocol {
		//doipv4 = false
	}

	for k, _ := range rib {
		if doipv4 && k.Is4() {
			return false
		}
		if doipv6 && k.Is6() {
			return false
		}
	}

	return true
}

func (u *update) message(rib map[netip.Addr]bool) []byte {

	next_hop_address := u.NextHop6[:] // should be 16 or 32 bytes - a global adddress or global+link-local pair
	hop := u.NextHop
	asn := u.ASNumber
	local_pref := u.LocalPref
	med := u.MED
	communities := u.Communities
	external := u.External

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

	// (Well-known, Mandatory, Transitive, Complete, Regular length). 2(AS_PATH), 0(bytes, if iBGP - may get updated)
	as_path := []byte{WTCR, AS_PATH, 0}

	if external {
		// Each AS path segment is represented by a triple <path segment type, path segment length, path segment value>
		as_sequence := []byte{AS_SEQUENCE, 1} // AS_SEQUENCE(2), 1 ASN
		as_sequence = append(as_sequence, htons(asn)...)
		as_path = append(as_path, as_sequence...)
		as_path[2] = byte(len(as_sequence)) // update length field
	}

	// (Well-known, Mandatory, Transitive, Complete, Regular length), NEXT_HOP(3), 4(bytes)
	next_hop := append([]byte{WTCR, NEXT_HOP, 4}, hop[:]...)

	path_attributes := []byte{}
	path_attributes = append(path_attributes, origin...)
	path_attributes = append(path_attributes, as_path...)
	path_attributes = append(path_attributes, next_hop...)

	// rfc4271: A BGP speaker MUST NOT include this attribute in UPDATE messages it sends to external peers ...
	if !external {

		if local_pref == 0 {
			local_pref = 100
		}

		// (Well-known, Transitive, Complete, Regular length), LOCAL_PREF(5), 4 bytes
		attr := append([]byte{WTCR, LOCAL_PREF, 4}, htonl(local_pref)...)
		path_attributes = append(path_attributes, attr...)
	}

	if len(communities) > 0 {
		comms := []byte{}
		for k, v := range communities {
			if k < 60 { // should implement extended length
				c := htonl(uint32(v))
				comms = append(comms, c[:]...)
			}
		}

		// (Optional, Transitive, Complete, Regular length), COMMUNITIES(8), n bytes
		attr := append([]byte{OTCR, COMMUNITIES, uint8(len(comms))}, comms...)
		path_attributes = append(path_attributes, attr...)
	}

	if med > 0 {
		// (Optional, Non-transitive, Complete, Regular length), MULTI_EXIT_DISC(4), 4 bytes
		attr := append([]byte{ONCR, MULTI_EXIT_DISC, 4}, htonl(uint32(med))...)
		path_attributes = append(path_attributes, attr...)
	}

	if len(advertise6) > 0 {
		// https://datatracker.ietf.org/doc/html/rfc2545
		mp_reach_nlri := []byte{0, 2, 1} // IPv6 unicast AFI 2, SAFI 1
		mp_reach_nlri = append(mp_reach_nlri, byte(len(next_hop_address)))
		mp_reach_nlri = append(mp_reach_nlri, next_hop_address...)
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
	update = append(update, htons(uint16(len(withdrawn)))...)
	update = append(update, withdrawn...)

	if len(advertise) > 0 || len(advertise6) > 0 || len(withdrawn6) > 0 {
		update = append(update, htons(uint16(len(path_attributes)))...)
		update = append(update, path_attributes...)
		update = append(update, advertise...)
	} else {
		update = append(update, 0, 0) // total path attribute length 0
	}

	return update
}

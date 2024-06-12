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

// A really stupid BGP4 implementation for avertising /32 addresses for load balancing

// https://datatracker.ietf.org/doc/html/rfc4271 - A Border Gateway Protocol 4 (BGP-4)
// https://datatracker.ietf.org/doc/html/rfc8203 - BGP Administrative Shutdown Communication
// https://datatracker.ietf.org/doc/html/rfc4486 - Subcodes for BGP Cease Notification Message

// https://datatracker.ietf.org/doc/html/rfc2918 - Route Refresh Capability for BGP-4

package bgp

import "fmt"

func htonl(h uint32) [4]byte {
	return [4]byte{byte(h >> 24), byte(h >> 16), byte(h >> 8), byte(h)}
}

func htons(h uint16) [2]byte {
	return [2]byte{byte(h >> 8), byte(h)}
}

const (
	M_OPEN         = 1
	M_UPDATE       = 2
	M_NOTIFICATION = 3
	M_KEEPALIVE    = 4

	IGP = 0
	EGP = 1

	//https://www.rfc-editor.org/rfc/rfc3392.txt
	CAPABILITIES_OPTIONAL_PARAMETER = 2 // Capabilities Optional Parameter (Parameter Type 2)

	// https://www.iana.org/assignments/capability-codes/capability-codes.xhtml
	BGP4_MP = 1 //Multiprotocol Extensions for BGP-4

	// Path attribute types
	ORIGIN          = 1
	AS_PATH         = 2
	NEXT_HOP        = 3
	MULTI_EXIT_DISC = 4
	LOCAL_PREF      = 5
	COMMUNITIES     = 8
	MP_REACH_NLRI   = 14 // Multiprotocol Reachable NLRI - MP_REACH_NLRI (Type Code 14)
	MP_UNREACH_NLRI = 15 // Multiprotocol Unreachable NLRI - MP_UNREACH_NLRI (Type Code 15)

	AS_SET      = 1
	AS_SEQUENCE = 2

	// NOTIFICATION ERROR CODES
	MESSAGE_HEADER_ERROR        = 1 // [RFC4271]
	OPEN_MESSAGE_ERROR          = 2 // [RFC4271]
	UPDATE_MESSAGE_ERROR        = 3 // [RFC4271]
	HOLD_TIMER_EXPIRED          = 4 // [RFC4271]
	FSM_ERROR                   = 5 // [RFC4271]
	CEASE                       = 6 // [RFC4271]
	ROUTE_REFRESH_MESSAGE_ERROR = 7 // [RFC7313]

	UNSUPPORTED_VERSION_NUMBER = 1 // OPEN_MESSAGE_ERROR
	BAD_BGP_ID                 = 3 // OPEN_MESSAGE_ERROR
	UNNACEPTABLE_HOLD_TIME     = 6 // OPEN_MESSAGE_ERROR
	BAD_MESSAGE_TYPE           = 3 // MESSAGE_HEADER_ERROR
	ADMINISTRATIVE_SHUTDOWN    = 2 // CEASE
	OUT_OF_RESOURCES           = 8 // CEASE

	// Optional/Well-known, Non-transitive/Transitive Complete/Partial Regular/Extended-length
	// 128 64 32 16 8 4 2 1
	// 0   1  0  1  0 0 0 0
	// W   N  C  R  0 0 0 0
	// O   T  P  E  0 0 0 0

	WTCR = 64  // (Well-known, Transitive, Complete, Regular length)
	WTCE = 80  // (Well-known, Transitive, Complete, Extended length)
	ONCR = 128 // (Optional, Non-transitive, Complete, Regular length)
	ONCE = 144 // (Optional, Non-transitive, Complete, Extended length)
	OTCR = 192 // (Optional, Transitive, Complete, Regular length)
	OTCE = 208 // (Optional, Transitive, Complete, Extended length)
)

// https://www.iana.org/assignments/bgp-parameters/bgp-parameters.xhtml#bgp-parameters-3
func (n *notification) note() string {

	var s string = "<unrecognised>"
	var sub string

	switch n.code {
	case 0: // Reserved - using this for "local" errors
		switch n.sub {
		case CONNECTION_FAILED:
			s = "Connection failed"
		case REMOTE_SHUTDOWN:
			s = "Remote shutdown"
		case LOCAL_SHUTDOWN:
			s = "Local shutdown"
		case INVALID_LOCALIP:
			s = "Invalid local IP"
		default:
			s = "Unknown"
		}

	case MESSAGE_HEADER_ERROR:
		s = "Message header error"
		switch n.sub {
		case 1:
			sub = "Connection Not Synchronized" // [RFC4271]
		case 2:
			sub = "Bad Message Length" // [RFC4271]
		case 3:
			sub = "Bad Message Type" // [RFC4271]
		}

	case OPEN_MESSAGE_ERROR:
		s = "OPEN Message Error"
		switch n.sub {
		case 1:
			sub = "Unsupported Version Number" // [RFC4271]
		case 2:
			sub = "Bad Peer AS" // [RFC4271]
		case 3:
			sub = "Bad BGP Identifier" // [RFC4271]
		case 4:
			sub = "Unsupported Optional Parameter" // [RFC4271]
		case 5:
			sub = "[Deprecated]" // [RFC4271]
		case 6:
			sub = "Unacceptable Hold Time" // [RFC4271]
		case 7:
			sub = "Unsupported Capability" // [RFC5492]
		case 8:
			sub = "Deprecated" // [RFC9234]
		case 9:
			sub = "Deprecated" // [RFC9234]
		case 10:
			sub = "Deprecated" // [RFC9234]
		case 11:
			sub = "Role Mismatch" // [RFC9234]
		}

	case UPDATE_MESSAGE_ERROR:
		s = "UPDATE Message Error"
		switch n.sub {
		case 1:
			sub = "Malformed Attribute List" // [RFC4271]
		case 2:
			sub = "Unrecognized Well-known Attribute" // [RFC4271]
		case 3:
			sub = "Missing Well-known Attribute" // [RFC4271]
		case 4:
			sub = "Attribute Flags Error" // [RFC4271]
		case 5:
			sub = "Attribute Length Error" // [RFC4271]
		case 6:
			sub = "Invalid ORIGIN Attribute" // [RFC4271]
		case 7:
			sub = "[Deprecated]" // [RFC4271]
		case 8:
			sub = "Invalid NEXT_HOP Attribute" // [RFC4271]
		case 9:
			sub = "Optional Attribute Error" // [RFC4271]
		case 10:
			sub = "Invalid Network Field" // [RFC4271]
		case 11:
			sub = "Malformed AS_PATH" // [RFC4271]
		}

	case FSM_ERROR:
		s = "BGP Finite State Machine Error"
		switch n.sub {
		case 0:
			sub = "Unspecified Error" // [RFC6608]
		case 1:
			sub = "Receive Unexpected Message in OpenSent State" // [RFC6608]
		case 2:
			sub = "Receive Unexpected Message in OpenConfirm State" // [RFC6608]
		case 3:
			sub = "Receive Unexpected Message in Established State" // [RFC6608]
		}

	case HOLD_TIMER_EXPIRED:
		s = "Hold timer expired"

	case CEASE:
		s = "Cease"
		switch n.sub {
		case 1:
			sub = "Maximum Number of Prefixes Reached" // [RFC4486]
		case 2:
			sub = "Administrative Shutdown" // [RFC4486][RFC9003]
		case 3:
			sub = "Peer De-configured" // [RFC4486]
		case 4:
			sub = "Administrative Reset" // [RFC4486][RFC9003]
		case 5:
			sub = "Connection Rejected" // [RFC4486]
		case 6:
			sub = "Other Configuration Change" // [RFC4486]
		case 7:
			sub = "Connection Collision Resolution" //	[RFC4486]
		case 8:
			sub = "Out of Resources" // [RFC4486]
		case 9:
			sub = "Hard Reset" // [RFC8538]
		case 10:
			sub = "BFD Down" // [RFC9384]
		}
	}

	if len(sub) > 0 {
		s += "; " + sub
	}

	if len(n.data) > 0 {
		s += " " + fmt.Sprint(n.data)
	}

	return s
}

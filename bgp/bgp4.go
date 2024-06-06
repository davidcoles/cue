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
	MESSAGE_HEADER_ERROR = 1
	OPEN_ERROR           = 2
	HOLD_TIMER_EXPIRED   = 4
	FSM_ERROR            = 5
	CEASE                = 6

	// MESSAGE_HEADER_ERROR
	BAD_MESSAGE_TYPE = 3

	// OPEN_ERROR
	UNSUPPORTED_VERSION_NUMBER = 1
	BAD_BGP_ID                 = 3
	UNNACEPTABLE_HOLD_TIME     = 6

	// CEASE
	MAXIMUM_PREFIXES_REACHED        = 1
	ADMINISTRATIVE_SHUTDOWN         = 2
	PEER_DECONFIGURED               = 3
	ADMINISTRATIVE_RESET            = 4
	CONNECTION_REJECTED             = 5
	OTHER_CONFIGURATION_CHANGE      = 6
	CONNECTION_COLLISION_RESOLUTION = 7
	OUT_OF_RESOURCES                = 8

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

func note(code, sub uint8) string {
	var s string = "<unrecognised>"
	switch code {
	case 0:
		switch sub {
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
		switch sub {
		case BAD_MESSAGE_TYPE:
			s += "; Bad message type"
		}
	case OPEN_ERROR:
		s = "OPEN_ERROR"
		switch sub {
		case UNSUPPORTED_VERSION_NUMBER:
			s += "; Unsupported version number"
		case BAD_BGP_ID:
			s += "; Bad BGP identifier"
		case UNNACEPTABLE_HOLD_TIME:
			s += "; Unnaceptable hold time"
		}

	case FSM_ERROR:
		s = "Finite state machine error"

	case HOLD_TIMER_EXPIRED:
		s = "Hold timer expired"

	case CEASE:
		s = "Cease"
		switch sub {
		case MAXIMUM_PREFIXES_REACHED:
			s += "; Maximum prefixes reached"
		case ADMINISTRATIVE_SHUTDOWN:
			s += "; Administrative shutdown"
		case PEER_DECONFIGURED:
			s += "; Peer deconfigured"
		case ADMINISTRATIVE_RESET:
			s += "; Administrative reset"
		case CONNECTION_REJECTED:
			s += "; Connection rejected"
		case OTHER_CONFIGURATION_CHANGE:
			s += "; Other configuration change"
		case CONNECTION_COLLISION_RESOLUTION:
			s += ": Connection collision resolution"
		case OUT_OF_RESOURCES:
			s += "; Out of resources"
		}
	}
	return s
}

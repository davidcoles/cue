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
	"fmt"
	"net/netip"
	"sync"
	"time"
)

const (
	IDLE         = "IDLE"
	ACTIVE       = "ACTIVE"
	CONNECT      = "CONNECT"
	OPEN_SENT    = "OPEN_SENT"
	OPEN_CONFIRM = "OPEN_CONFIRM"
	ESTABLISHED  = "ESTABLISHED"
)

type Status struct {
	State             string        `json:"state"`
	When              time.Time     `json:"when"`
	Duration          time.Duration `json:"duration_s"`
	UpdateCalculation time.Duration `json:"update_calculation_ms"`
	Advertised        uint64        `json:"advertised_routes"`
	Withdrawn         uint64        `json:"withdrawn_routes"`
	Prefixes          int           `json:"current_routes"`
	Attempts          uint64        `json:"connection_attempts"`
	Connections       uint64        `json:"successful_connections"`
	Established       uint64        `json:"established_sessions"`
	LastError         string        `json:"last_error"`
	HoldTime          uint16        `json:"hold_time"`
	LocalASN          uint16        `json:"local_asn"`
	RemoteASN         uint16        `json:"remote_asn"`
	AdjRIBOut         []string      `json:"adj_rib_out"`
	LocalIP           string        `json:"local_ip"`
}

const (
	CONNECTION_FAILED = iota
	REMOTE_SHUTDOWN
	LOCAL_SHUTDOWN
	INVALID_LOCALIP
)

type Session struct {
	c      chan Update
	p      Parameters
	rib    []netip.Addr
	status Status
	mutex  sync.Mutex
	update Update
	logs   BGPNotify
}

func (s *Session) log() BGPNotify {
	if s.logs == nil {
		return &nul{}
	}
	return s.logs
}

func toaddr(in []IP) (out []netip.Addr) {
	for _, i := range in {
		out = append(out, netip.AddrFrom4(i))
	}
	return
}

func NewSession(id IP, peer string, p Parameters, r []IP, l BGPNotify) *Session {
	s := &Session{p: p, rib: toaddr(r), logs: l, status: Status{State: IDLE}, update: newupdate(p, r)}
	s.c = s.session(id, peer)
	return s
}

func (s *Session) Start(id IP, peer string, p Parameters, r []netip.Addr, l BGPNotify) {
	s.p = p
	s.rib = r
	s.logs = l
	s.status = Status{State: IDLE}
	s.update = newupdate2(p, r)
	s.c = s.session(id, peer)
}

func (s *Session) Status() Status {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.status.Duration = time.Now().Sub(s.status.When) / time.Second
	return s.status
}

func (s *Session) RIB(r []IP) {
	s.rib = toaddr(r)
	s.c <- newupdate2(s.p, s.rib)
}

func (s *Session) LocRIB(r []netip.Addr) {
	s.rib = r
	s.c <- newupdate2(s.p, s.rib)
}

func (s *Session) Configure(p Parameters) {
	s.p = p
	s.c <- newupdate2(s.p, s.rib)
}

func (s *Session) Close() {
	close(s.c)
}

func (s *Session) Stop() {
	close(s.c)
}

func (s *Session) state2(state string) {
	s.status.State = state
	s.status.When = time.Now().Round(time.Second)
}

func (s *Session) state(state string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.state2(state)
}

func (s *Session) error(error string) string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.status.LastError = error
	return error
}

func (s *Session) established(ht uint16, local, remote uint16) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.state2(ESTABLISHED)
	s.status.Established++
	s.status.LastError = ""
	s.status.HoldTime = ht
	s.status.LocalASN = local
	s.status.RemoteASN = remote
}

func (s *Session) active(ht uint16, local uint16, ip [4]byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.state2(ACTIVE)
	s.status.Attempts++

	s.status.AdjRIBOut = nil
	s.status.Prefixes = 0
	s.status.Advertised = 0
	s.status.Withdrawn = 0
	s.status.HoldTime = ht
	s.status.LocalASN = local
	s.status.RemoteASN = 0
	s.status.LocalIP = ip_string(ip)
}
func (s *Session) connect() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.state2(CONNECT)
	s.status.Connections++
}

func (s *Session) update_stats(d time.Duration, r []netip.Addr, n map[netip.Addr]bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	var a, w uint64

	var rib []string
	for _, ip := range r {
		rib = append(rib, ip.String())
	}

	for _, v := range n {
		if v {
			a++
		} else {
			w++
		}
	}

	s.status.Advertised += a
	s.status.Withdrawn += w
	s.status.UpdateCalculation = d / time.Millisecond
	s.status.AdjRIBOut = rib
	s.status.Prefixes = len(r)
}

func (s *Session) session(id IP, peer string) chan Update {
	const F = "session"

	updates := make(chan Update, 10)

	go func() {

		retry_time := 30 * time.Second

		timer := time.NewTimer(1) // fires immediately
		defer timer.Stop()

		var ok bool

		for {
			select {
			case <-timer.C:
				s.log().BGPSession(peer, true, "Connecting ...")
				b, n := s.try(id, peer, updates)
				var e string

				if b {
					e = fmt.Sprintf("Received notification[%d:%d]: %s", n.code, n.sub, note(n.code, n.sub))
					if len(n.data) > 0 {
						e += " (" + string(n.data) + ")"
					}

					s.log().BGPSession(peer, false, e)

				} else {
					if n.code == 0 {
						e = note(n.code, n.sub)
					} else {
						e = fmt.Sprintf("Sent notification[%d:%d]: %s", n.code, n.sub, note(n.code, n.sub))
					}
					if len(n.data) > 0 {
						e += " (" + string(n.data) + ")"
					}

					if n.code == 0 && n.sub == LOCAL_SHUTDOWN {
						s.log().BGPSession(peer, true, e)
					} else {
						s.log().BGPSession(peer, false, e) // treat as "remote" as it was a failed connection, not a local shutdown
					}
				}

				s.error(e)
				s.idle()
				timer.Reset(retry_time)

			case s.update, ok = <-updates: // stores last update
				if !ok {
					return
				}
			}
		}

	}()

	return updates
}

func (s *Session) idle() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	//s.status.State = IDLE
	s.state2(IDLE)
}

func (s *Session) try(routerid IP, peer string, updates chan Update) (bool, notification) {

	nexthop4 := s.update.Parameters.NextHop4
	nexthop6 := s.update.Parameters.NextHop6
	multiprotocol := s.update.Parameters.Multiprotocol

	asnumber := s.update.Parameters.ASNumber
	holdtime := s.update.Parameters.HoldTime
	sourceip := s.update.Parameters.SourceIP
	localip := sourceip // may be 0.0.0.0 - in which case network stack chooses address/interface

	var external bool

	if holdtime < 3 {
		holdtime = 10
	}

	s.active(holdtime, asnumber, localip)

	conn, err := new_connection2(localip, peer)

	if err != nil {
		return false, local(CONNECTION_FAILED, err.Error())
	}

	defer conn.Close()

	var local6 [16]byte

	loc, ok := conn.Local()

	if !ok {
		return false, local(INVALID_LOCALIP, "No local address")
	}

	var ipv6 bool

	var localaddr string

	if len(loc) == 4 {
		copy(localip[:], loc[:])
		localaddr = netip.AddrFrom4(localip).String()
	} else if len(loc) == 16 {
		copy(local6[:], loc[:])
		ipv6 = true
		localaddr = netip.AddrFrom16(local6).String()
	} else {
		return false, local(INVALID_LOCALIP, "No local address")
	}

	s.mutex.Lock()
	s.status.HoldTime = holdtime
	s.status.LocalIP = localaddr
	s.mutex.Unlock()

	s.connect()

	o := open{asNumber: asnumber, holdTime: holdtime, routerID: routerid, multiprotocol: true}
	//conn.queue(M_OPEN, o.message())
	conn.queue2(&o)

	s.state(OPEN_SENT)

	hold_time_ns := time.Duration(holdtime) * time.Second
	hold_timer := time.NewTimer(hold_time_ns)
	defer hold_timer.Stop()

	keepalive_time_ns := hold_time_ns / 3
	keepalive_timer := time.NewTicker(keepalive_time_ns)
	defer keepalive_timer.Stop()

	var nul4 IP4
	var nul6 IP6

	if nexthop4 == nul4 {
		nexthop4 = localip
	}

	if nexthop6 == nul6 {
		nexthop6 = local6
	}

	if nexthop4 == nul4 {
		// fall back to routerid if we have nothing better for ipv4 next hop
		//  should only happen if the session was established over IPv6
		nexthop4 = routerid
	}

	var nlri map[netip.Addr]bool
	var adjRIBOut []netip.Addr
	var parameters Parameters

	notify := func(code, sub byte) notification {
		n := notification{code: code, sub: sub}
		conn.queue2(&n)
		return n
	}

	updateTemplate := update{
		IPv6:          ipv6,
		ASNumber:      asnumber,
		External:      external,
		NextHop:       nexthop4,
		NextHop6:      nexthop6,
		Multiprotocol: multiprotocol,
	}

	for {
		select {
		case m, ok := <-conn.C:

			if !ok {
				return false, local(REMOTE_SHUTDOWN, conn.Error)
			}

			hold_timer.Reset(hold_time_ns)

			switch m.Type() {
			case M_NOTIFICATION:
				n, _ := m.(*notification)
				return true, *n

			case M_KEEPALIVE:
				if s.status.State == OPEN_SENT {
					return false, notify(FSM_ERROR, 0)
				}

			case M_OPEN:
				o, ok := m.(*open)
				if !ok {
					return false, notify(FSM_ERROR, 0)
				}

				if s.status.State != OPEN_SENT {
					return false, notify(FSM_ERROR, 0)
				}

				//if m.open.version != 4 {
				if o.version != 4 {
					return false, notify(OPEN_ERROR, UNSUPPORTED_VERSION_NUMBER)
				}

				if o.holdTime < 3 {
					return false, notify(OPEN_ERROR, UNNACEPTABLE_HOLD_TIME)
				}

				if o.routerID == routerid {
					return false, notify(OPEN_ERROR, BAD_BGP_ID)
				}

				if o.holdTime < holdtime {
					holdtime = o.holdTime
					hold_time_ns = time.Duration(holdtime) * time.Second
					keepalive_time_ns = hold_time_ns / 3
				}

				hold_timer.Reset(hold_time_ns)
				keepalive_timer.Reset(keepalive_time_ns)

				external = o.asNumber != asnumber

				s.established(holdtime, asnumber, o.asNumber)

				conn.queue2(&keepalive{})

				t := time.Now()
				p := s.update.Parameters
				u := updateTemplate.withParameters(p)

				// initial NLRI will simply advertise any initial addresses in the RIB
				adjRIBOut, nlri = NLRI(s.update.adjRIBOut(ipv6), nil, false)
				parameters = p

				//fmt.Println("Init:", adjRIBOut, nlri)

				if len(nlri) > 0 {
					if updates := u.updates(nlri); len(updates) < 1 {
						return false, notify(CEASE, OUT_OF_RESOURCES)
					} else {
						conn.queue2(updates...)
					}
				}

				s.update_stats(time.Now().Sub(t), adjRIBOut, nlri)

			case M_UPDATE:
				if s.status.State != ESTABLISHED {
					return false, notify(FSM_ERROR, 0)
				}
				// we don't process update contents because we don't need to do any routing

			default:
				return false, notify(MESSAGE_HEADER_ERROR, BAD_MESSAGE_TYPE)
			}

		case r, ok := <-updates:

			if !ok {
				return false, notify(CEASE, ADMINISTRATIVE_SHUTDOWN)
			}

			if s.status.State == ESTABLISHED {
				t := time.Now()
				p := r.Parameters
				u := updateTemplate.withParameters(p)

				// calculate NLRI to transmit - force re-advertisement if parameters have changed (MED, local-pref, communities)
				adjRIBOut, nlri = NLRI(r.adjRIBOut(ipv6), adjRIBOut, parameters.Diff(p))
				parameters = p

				//fmt.Println("Update:", adjRIBOut, nlri)

				if len(nlri) > 0 {
					if updates := u.updates(nlri); len(updates) < 1 {
						return false, notify(CEASE, OUT_OF_RESOURCES)
					} else {
						conn.queue2(updates...)
					}
				}

				s.update_stats(time.Now().Sub(t), adjRIBOut, nlri)
			}

			s.update = r

		case <-keepalive_timer.C:
			if s.status.State == ESTABLISHED {
				conn.queue2(&keepalive{})
			}

		case <-hold_timer.C:
			return false, notify(HOLD_TIMER_EXPIRED, 0)
		}
	}

}

func local(s uint8, d string) notification {
	return notification{code: 0, sub: s, data: []byte(d)}
}

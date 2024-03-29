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
	//EBGP              bool          `json:""`
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
	r      []IP
	status Status
	mutex  sync.Mutex
	update Update
	log    BGPNotify
}

func NewSession(id IP, peer string, p Parameters, r []IP, l BGPNotify) *Session {
	s := &Session{p: p, r: r, log: l, status: Status{State: IDLE}, update: Update{RIB: r, Parameters: p}}
	s.c = s.session(id, peer)
	return s

}

func (s *Session) Status() Status {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.status.Duration = time.Now().Sub(s.status.When) / time.Second
	return s.status
}

func (s *Session) RIB(r []IP) {
	s.r = r
	s.c <- Update{RIB: s.r, Parameters: s.p}
}

func (s *Session) Configure(p Parameters) {
	s.p = p
	s.c <- Update{RIB: s.r, Parameters: s.p}
}

func (s *Session) Close() {
	close(s.c)
}

func (s *Session) state2(state string) {
	s.status.State = state
	s.status.When = time.Now().Round(time.Second)
}

func (s *Session) state(state string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	//s.status.State = state
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
	//s.status.State = ESTABLISHED
	s.state2(ESTABLISHED)
	s.status.Established++
	s.status.LastError = ""
	s.status.HoldTime = ht
	s.status.LocalASN = local
	s.status.RemoteASN = remote
	//s.status.EBGP = local != remote
}

func (s *Session) active(ht uint16, local uint16, ip [4]byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	//s.status.State = ACTIVE
	s.state2(ACTIVE)
	s.status.Attempts++

	s.status.AdjRIBOut = nil
	s.status.Prefixes = 0
	s.status.Advertised = 0
	s.status.Withdrawn = 0
	s.status.HoldTime = ht
	s.status.LocalASN = local
	s.status.RemoteASN = 0
	//s.status.EBGP = false
	s.status.LocalIP = ip_string(ip)
}
func (s *Session) connect() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	//s.status.State = CONNECT
	s.state2(CONNECT)
	s.status.Connections++
}

func (s *Session) update_stats(a, w uint64, d time.Duration, r []string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.status.Advertised += a
	s.status.Withdrawn += w
	s.status.UpdateCalculation = d / time.Millisecond
	s.status.AdjRIBOut = r
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
				s.log.BGPSession(peer, true, "Connecting ...")
				b, n := s.try(id, peer, updates)
				var e string

				if b {
					e = fmt.Sprintf("Received notification[%d:%d]: %s", n.code, n.sub, note(n.code, n.sub))
					if len(n.data) > 0 {
						e += " (" + string(n.data) + ")"
					}

					s.log.BGPSession(peer, false, e)

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
						s.log.BGPSession(peer, true, e)
					} else {
						s.log.BGPSession(peer, false, e) // treat as "remote" as it was a failed connection, not a local shutdown
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

	asnumber := s.update.Parameters.ASNumber
	holdtime := s.update.Parameters.HoldTime
	sourceip := s.update.Parameters.SourceIP
	localip := sourceip // may be 0.0.0.0 - in which case network stack chooses address/interface

	var external bool

	if holdtime < 3 {
		holdtime = 10
	}

	s.active(holdtime, asnumber, localip)

	conn, err := new_connection(localip, peer)

	if err != nil {
		return false, local(CONNECTION_FAILED, err.Error())
	}

	defer conn.Close()

	var nul [4]byte

	localip = conn.Local()

	if localip == nul {
		return false, local(INVALID_LOCALIP, err.Error())
	}

	//s.active(holdtime, asnumber, localip) // locks mutex
	s.mutex.Lock()
	s.status.HoldTime = holdtime
	s.status.LocalIP = ip_string(localip)
	s.mutex.Unlock()

	nexthop := localip

	s.connect()

	conn.write(openMessage(asnumber, holdtime, routerid))

	s.state(OPEN_SENT)

	hold_time_ns := time.Duration(holdtime) * time.Second
	hold_timer := time.NewTimer(hold_time_ns)
	defer hold_timer.Stop()

	keepalive_time_ns := hold_time_ns / 3
	keepalive_timer := time.NewTicker(keepalive_time_ns)
	defer keepalive_timer.Stop()

	for {
		select {
		case m, ok := <-conn.C:

			if !ok {
				return false, local(REMOTE_SHUTDOWN, conn.Error)
			}

			hold_timer.Reset(hold_time_ns)

			switch m.mtype {
			case M_NOTIFICATION:
				return true, m.notification

			case M_KEEPALIVE:
				if s.status.State == OPEN_SENT {
					n := notificationMessage(FSM_ERROR, 0)
					conn.write(n)
					return false, n.notification
				}

			case M_OPEN:
				if s.status.State != OPEN_SENT {
					n := notificationMessage(FSM_ERROR, 0)
					conn.write(notificationMessage(FSM_ERROR, 0))
					return false, n.notification
				}

				if m.open.version != 4 {
					n := notificationMessage(OPEN_ERROR, UNSUPPORTED_VERSION_NUMBER)
					conn.write(n)
					return false, n.notification
				}

				if m.open.ht < 3 {
					n := notificationMessage(OPEN_ERROR, UNNACEPTABLE_HOLD_TIME)
					conn.write(n)
					return false, n.notification
				}

				if m.open.id == routerid {
					n := notificationMessage(OPEN_ERROR, BAD_BGP_ID)
					conn.write(n)
					return false, n.notification
				}

				if m.open.ht < holdtime {
					holdtime = m.open.ht
					hold_time_ns = time.Duration(holdtime) * time.Second
					keepalive_time_ns = hold_time_ns / 3
				}

				hold_timer.Reset(hold_time_ns)
				keepalive_timer.Reset(keepalive_time_ns)

				external = m.open.as != asnumber

				s.established(holdtime, asnumber, m.open.as)

				conn.write(keepaliveMessage())

				t := time.Now()
				aro, p := s.update.adjRIBOutP()
				//conn.write(updateMessage(nexthop, asnumber, p, external, advertise(aro)))
				conn.write(_updateMessage(nexthop, asnumber, p.LocalPref, p.MED, p.Communities, external, advertise(aro)))
				s.update_stats(uint64(len(aro)), 0, time.Now().Sub(t), to_string(aro))

			case M_UPDATE:
				if s.status.State != ESTABLISHED {
					n := notificationMessage(FSM_ERROR, 0)
					conn.write(n)
					return false, n.notification
				}
				// we just ignore updates!

			default:
				n := notificationMessage(MESSAGE_HEADER_ERROR, BAD_MESSAGE_TYPE)
				conn.write(n)
				return false, n.notification
			}

		case r, ok := <-updates:

			if !ok {
				//conn.write(notificationMessage(CEASE, ADMINISTRATIVE_SHUTDOWN))
				conn.write(shutdownMessage("That's all, folks!"))
				return false, local(LOCAL_SHUTDOWN, "")
			}

			if s.status.State == ESTABLISHED {
				t := time.Now()
				a, w, nlris := r.updates(s.update)
				if len(nlris) != 0 {
					//conn.write(updateMessage(nexthop, asnumber, r.Parameters, external, nlris))
					p := r.Parameters
					conn.write(_updateMessage(nexthop, asnumber, p.LocalPref, p.MED, p.Communities, external, nlris))
				}
				s.update_stats(a, w, time.Now().Sub(t), r.adjRIBOutString())
			}

			s.update = r

		case <-keepalive_timer.C:
			if s.status.State == ESTABLISHED {
				conn.write(keepaliveMessage())
			}

		case <-hold_timer.C:
			n := notificationMessage(HOLD_TIMER_EXPIRED, 0)
			conn.write(n)
			return false, n.notification
		}
	}

}

func local(s uint8, d string) notification {
	return notification{code: 0, sub: s, data: []byte(d)}
}

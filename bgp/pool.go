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

	"github.com/davidcoles/cue/log"
)

type logger = log.Log

type KV = map[string]any

type status = map[string]Status

type Pool struct {
	c chan map[string]Parameters
	r chan []IP
	s chan chan status
	l logger
}

func (p *Pool) log() logger {
	l := p.l

	if l != nil {
		return l
	}

	return &log.Nil{}
}

func (p *Pool) Status() status {
	c := make(chan status)
	p.s <- c
	return <-c
}

func (p *Pool) Configure(c map[string]Parameters) {
	p.c <- c
}

func (p *Pool) _RIB(r []IP) {
	p.r <- r
}

func (p *Pool) RIB(r []netip.Addr) {
	var f []IP

	for _, a := range r {
		if a.Is4() {
			f = append(f, a.As4())
		}
	}

	p.r <- f
}

func (p *Pool) Close() {
	close(p.c)
}

func dup(i []IP) (o []IP) {
	for _, x := range i {
		o = append(o, x)
	}
	return
}

func NewPool(routerid IP, peers map[string]Parameters, rib_ []IP, log logger) *Pool {
	const F = "pool"

	var nul IP

	rib := dup(rib_)

	if routerid == nul {
		return nil
	}

	pool := &Pool{c: make(chan map[string]Parameters), r: make(chan []IP), s: make(chan chan status), l: log}

	go func() {

		sessions := map[string]*Session{}

		defer func() {
			for _, session := range sessions {
				session.Close()
			}
		}()

		for {
			select {
			case c := <-pool.s:
				s := map[string]Status{}
				for peer, session := range sessions {
					s[peer] = session.Status()
				}
				c <- s

			case r := <-pool.r:

				rib = dup(r)

				for _, session := range sessions {
					session.RIB(rib)
				}

			case i, ok := <-pool.c:

				if !ok {
					return
				}

				for peer, params := range i {
					if session, ok := sessions[peer]; ok {
						session.Configure(params)
					} else {
						//pool.log().NOTICE(F, "New peer", routerid, peer, params, rib)
						pool.log().NOTICE(F, KV{"event": "new-peer", "peer": peer, "params": params, "rib": rib})
						sessions[peer] = NewSession(routerid, peer, params, rib, pool.log())
					}
				}

				// if any sessions don't appear in the config map then close and remove them
				for peer, session := range sessions {
					if _, ok := i[peer]; !ok {
						session.Close()
						delete(sessions, peer)
						pool.log().NOTICE(F, KV{"event": "deleted-peer", "peer": peer})
					}
				}
			}
		}
	}()

	pool.c <- peers

	return pool
}

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

package cue

import (
	"errors"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/davidcoles/cue/mon"
)

const (
	TCP = 0x06
	UDP = 0x11
)

type Check = mon.Check
type Scheduler = uint8

type Service struct {
	Address      netip.Addr
	Port         uint16
	Protocol     uint8
	Scheduler    Scheduler //TODO
	Sticky       bool
	Required     uint8
	Destinations []Destination
	available    uint8
	Up           bool
	When         time.Time
}

type Destination struct {
	Address  netip.Addr  `json:"address"`
	Port     uint16      `json:"port"`
	Disabled bool        `json:"disabled"`
	Weight   uint8       `json:"weight"`
	Status   mon.Status  `json:"status"`
	Checks   []mon.Check `json:"checks"`
}

type Balancer interface {
	Configure([]Service) error
}

type protocol uint8
type tuple = mon.Service
type nilBalancer struct{}

func (b *nilBalancer) Configure([]Service) error { return nil }

// If the destination is healthy then this function returns its weight. If unhealthy or disabled, zero is returned
func (d *Destination) HealthyWeight() uint8 {
	if !d.Disabled && d.Status.OK && d.Weight > 0 {
		return 1
	}
	return 0
}

func (p protocol) MarshalText() ([]byte, error) {
	switch p {
	case TCP:
		return []byte("TCP"), nil
	case UDP:
		return []byte("UDP"), nil
	}
	return []byte("Unknown"), nil
}

func (s *Service) Available() uint8 {
	return s.available
}

func (s *Service) Healthy() bool {
	return s.available >= s.Required
}

func (i Service) less(j Service) bool {
	if r := i.Address.Compare(j.Address); r != 0 {
		return r < 0
	}

	if i.Port != j.Port {
		return i.Port < j.Port
	}

	if i.Protocol != j.Protocol {
		return i.Protocol < j.Protocol
	}

	return false
}

type Director struct {
	// A channel which may be used to receive notifications of changes in status of backend servers.
	C chan bool

	// The Balancer which will implement the services managed by this Director.
	Balancer Balancer

	// Default IP address to use for network probes (needed for SYN, should be optional).
	Address netip.Addr

	mutex sync.Mutex
	cfg   map[tuple]Service
	mon   *mon.Mon
	die   chan bool

	svc map[tuple]status
}

type status struct {
	up   bool
	time time.Time
}

func (d *Director) Start(cfg []Service) (err error) {

	d.C = make(chan bool, 1)

	// start monitoring with an empty set of services (nil)
	d.mon, err = mon.New(d.Address, nil, d.Balancer)

	if err != nil {
		return err
	}

	err = d.Configure(cfg)

	if err != nil {
		d.mon.Update(nil)
		return err
	}

	d.die = make(chan bool)

	go d.background()

	return nil
}

func (d *Director) Stop() {
	close(d.die)
}

func (d *Director) Configure(config []Service) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	cfg := map[tuple]Service{}

	for _, s := range config {
		t := tuple{Address: s.Address, Port: s.Port, Protocol: s.Protocol}
		cfg[t] = s
	}

	vips := map[netip.Addr]bool{}
	svcs := map[mon.Service]bool{}

	// scan previous config for checks to see if vip/service existed ...
	for s, _ := range d.cfg {
		vips[s.Address] = true
		svcs[mon.Service{Address: s.Address, Port: s.Port, Protocol: s.Protocol}] = true
	}

	services := map[mon.Instance]mon.Target{}

	for _, s := range cfg {
		if s.Port == 0 {
			return errors.New("Service port cannot be 0")
		}

		if s.Protocol != TCP && s.Protocol != UDP {
			return errors.New("Only TCP and UDP protocols supported")
		}

		for _, d := range s.Destinations {
			if d.Port == 0 {
				return errors.New("Destination port cannot be 0")
			}
		}
	}

	for _, s := range cfg {

		service := mon.Service{Address: s.Address, Port: s.Port, Protocol: s.Protocol}

		// When:
		// 1) adding a new vip, all checks should start as down(false) to prevent routing flaps
		// 2) adding a new service to an existing vip, start up(true) to prevent vip being withdrawn
		// 3) adding a new real to an existing service, start as down(false) state to prevent rehash

		init := vips[s.Address] && !svcs[service]
		// 1: false && ?????? => false
		// 2: true  && !false => true
		// 3: true  && !true  => false

		for _, d := range s.Destinations {
			i := mon.Instance{Service: service, Destination: mon.Destination{Address: d.Address, Port: d.Port}}
			services[i] = mon.Target{Init: init, Checks: d.Checks}
		}
	}

	d.cfg = cfg

	// balancer update should return a bool/error value to inidcate if the config was acceptable
	// only do d.cfg = cfg if it was
	//d.balancer().Configure(config)
	//d.mon.Update(services)
	//d.inform()

	d.mon.Update(services)
	d.update()

	// TODO
	// save old monitring config
	// build new monitoring config
	// apply new monitoring
	// apply new config to balancer
	// if not rejected persist blancer + monitoring
	// if rejected, restore old monitoring

	return nil
}

func clone(in []Service) (out []Service) {

	for _, s := range in {
		c := s

		c.Destinations = nil
		for _, d := range s.Destinations {
			c.Destinations = append(c.Destinations, d)
		}

		out = append(out, c)
	}

	return out
}

func (d *Director) services() (r []Service) {

	m := d.mon

	svc := map[tuple]status{}

	for _, s := range d.cfg {

		var available uint8
		var destinations []Destination

		t := tuple{Address: s.Address, Port: s.Port, Protocol: s.Protocol}

		for _, d := range s.Destinations {

			status, _ := m.Status(t, mon.Destination{Address: d.Address, Port: d.Port})

			d.Status = status

			if d.HealthyWeight() > 0 {
				available++
			}

			destinations = append(destinations, d)
		}

		s.Destinations = destinations
		s.available = available

		state, ok := d.svc[t]

		up := s.Healthy()

		if !ok || state.up != up {
			state.time = time.Now()
		}

		state.up = up

		svc[t] = state

		s.Up = state.up
		s.When = state.time

		r = append(r, s)
	}

	d.svc = svc

	return
}

func (d *Director) status() (services []Service) {

	services = d.services()

	sort.SliceStable(services, func(i, j int) bool { return services[i].less(services[j]) })

	return services
}

func (d *Director) Status() (services []Service) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	return d.status()
}
func (d *Director) Trigger() {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.update()
}
func (d *Director) update() {
	d.balancer().Configure(d.status())
	d.inform()
}

func (d *Director) inform() {
	select {
	case d.C <- true:
	default:
	}
}

func (d *Director) background() {
	for {
		select {
		case <-d.mon.C:
			d.mutex.Lock()
			d.update()
			d.mutex.Unlock()
		case <-d.die:
			d.Configure(nil)
			d.mon.Update(nil)
			d.inform()
			return
		}
	}
}

func (d *Director) balancer() Balancer {
	b := d.Balancer

	if b == nil {
		return &nilBalancer{}
	}

	return b
}

func AllVIPs(services []Service) (r []netip.Addr) {
	vips := map[netip.Addr]bool{}

	for _, s := range services {
		vips[s.Address] = true
	}

	for v, _ := range vips {
		r = append(r, v)
	}

	return
}

func HealthyVIPs(services []Service) (r []netip.Addr) {

	vips := map[netip.Addr]bool{}

	for _, s := range services {
		vip := s.Address

		if s.Up {
			if _, ok := vips[vip]; !ok {
				vips[vip] = true
			}
		} else {
			vips[vip] = false
		}
	}

	for ip, ok := range vips {
		if ok {
			r = append(r, ip)
		}
	}

	return
}

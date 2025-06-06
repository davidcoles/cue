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

package mon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

//var client *http.Client
var cache map[netip.Addr]*sni
var mutex sync.Mutex

type sni struct {
	time   time.Time
	client *http.Client
}

func cacheClient(addr netip.Addr) *http.Client {
	mutex.Lock()
	defer mutex.Unlock()

	v, ok := cache[addr]

	if !ok {
		v = &sni{client: ipClient(addr)}
		cache[addr] = v
	}

	v.time = time.Now()

	return v.client
}

func init() {

	/*
		client = &http.Client{
			Timeout: time.Second * 2,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				Dial: (&net.Dialer{
					Timeout: 2 * time.Second,
				}).Dial,
				TLSHandshakeTimeout: 2 * time.Second,
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			},
		}
	*/

	// intialise a cache of per-IP clients
	cache = make(map[netip.Addr]*sni)

	// periodically check if per-IP clients have been used recently and remove if they haven't
	go func() {
		ticker := time.NewTicker(time.Second * 20)
		for {
			<-ticker.C
			mutex.Lock()
			now := time.Now()
			for k, v := range cache {
				if now.Sub(v.time) > time.Minute {
					v.client.CloseIdleConnections()
					delete(cache, k)
				}
			}
			mutex.Unlock()
		}
	}()
}

type Service struct {
	Address  netip.Addr
	Port     uint16
	Protocol uint8
}

type Destination struct {
	Address netip.Addr
	Port    uint16
}

type scheme bool

const (
	GET  method = false
	HEAD method = true
	UDP  method = false
	TCP  method = true
)

type Instance struct {
	Service     Service
	Destination Destination
}

type Services map[Instance]Checks

type Target struct {
	Init   bool
	Checks Checks
}

type state struct {
	mutex  sync.Mutex
	checks chan Checks
	status status
}

type status = Status
type Status struct {
	OK          bool
	Diagnostic  string
	Took        time.Duration
	Last        time.Time
	When        time.Time
	Initialised bool
}

type Prober interface {
	Probe(*Mon, Instance, Check) (bool, string)
}

type Notifier interface {
	Notify(Instance, bool)
	Result(Instance, bool, string)
	Check(Instance, string, uint64, bool, string)
}

type Mon struct {
	C                    chan bool  // Used to signal changes in monitored service status
	Prober               Prober     // Override standard probing functionalitry
	Notifier             Notifier   // For logging
	CloseIdleConnections bool       // Call CloseIdleConnections on http.Client after probe if true
	IPv4                 netip.Addr // IP address to use as source for SYN probes (optional)

	services map[Instance]*state
	syn      *SYN
}

func (m *Mon) Start(addr netip.Addr, services map[Instance]Target) error {
	m.C = make(chan bool, 1)
	m.services = make(map[Instance]*state)

	var nul netip.Addr
	if addr != nul {
		var err error
		m.syn, err = Syn(addr, false)

		if err != nil {
			return err
		}
	}

	m.Update(services)

	return nil
}

// notifier Notifier, prober Prober

func (m *Mon) Init(services map[Instance]Target) error {

	m.C = make(chan bool, 1)

	var nul netip.Addr
	if m.IPv4 != nul {
		var err error
		m.syn, err = Syn(m.IPv4, false)

		if err != nil {
			return err
		}
	}

	m.Update(services)

	return nil
}

func New(addr netip.Addr, services map[Instance]Target, notifier Notifier, prober Prober) (*Mon, error) {

	m := &Mon{C: make(chan bool, 1), services: make(map[Instance]*state), Prober: prober, Notifier: notifier}

	var nul netip.Addr
	if addr != nul {
		var err error
		m.syn, err = Syn(addr, false)

		if m.syn == nil {
			return nil, err
		}
	}

	m.Update(services)

	return m, nil
}

func (m *Mon) Status(svc Service, dst Destination) (status Status, _ bool) {
	s, ok := m.services[Instance{Service: svc, Destination: dst}]

	if !ok {
		return status, ok
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.status, ok
}

func (m *Mon) Stop() {
	m.Update(nil)
}

func (m *Mon) Update(checks map[Instance]Target) {

	for instance, state := range m.services {
		if new, ok := checks[instance]; ok {
			state.checks <- new.Checks
			delete(checks, instance)
		} else {
			close(state.checks) // no longer exists
			delete(m.services, instance)
		}
	}

	for instance, c := range checks {
		state := &state{status: status{OK: c.Init, Diagnostic: "Initialising ...", When: time.Now()}}
		state.checks = m.monitor(instance, state, c.Checks)
		m.services[instance] = state
	}

	select {
	case m.C <- true:
	default:
	}
}

func (m *Mon) notify(instance Instance, state bool) {
	if n := m.Notifier; n != nil {
		n.Notify(instance, state)
	}
}

func (m *Mon) result(instance Instance, state bool, result string) {
	if n := m.Notifier; n != nil {
		n.Result(instance, state, result)
	}
}

func (m *Mon) check(instance Instance, check string, round uint64, state bool, result string) {
	if n := m.Notifier; n != nil {
		n.Check(instance, check, round, state, result)
	}
}

func (m *Mon) monitor(instance Instance, state *state, c Checks) chan Checks {

	C := make(chan Checks, 10)

	m.notify(instance, state.status.OK)

	go func() {

		var history [5]bool

		if state.status.OK {
			history = [5]bool{true, true, true, true, true}
		}

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		var round uint64

		for {
			round++

			var ok bool
			select {
			case c, ok = <-C:
				if !ok {
					return
				}
				continue // go back and wait for ticker
			case <-ticker.C:
			}

			state.mutex.Lock()
			was := state.status
			state.mutex.Unlock()

			now := was

			t := time.Now()

			ok, now.Diagnostic = m.probes(instance, c, round)

			m.result(instance, ok, now.Diagnostic)

			copy(history[0:], history[1:])
			history[4] = ok

			var passed int
			for _, v := range history {
				if v {
					passed++
				}
			}

			if was.OK {
				if passed < 4 {
					now.OK = false
				}
			} else {
				if passed > 4 {
					now.OK = true
				}
			}

			now.Last = t
			now.Took = time.Now().Sub(t)
			now.Initialised = true

			var changed bool
			if !was.Initialised || was.OK != now.OK {
				if was.Initialised {
					m.notify(instance, now.OK)
				}
				changed = true
				now.When = t
			}

			state.mutex.Lock()
			state.status = now
			state.mutex.Unlock()

			if changed {
				select {
				case m.C <- true:
				default:
				}
			}

		}
	}()

	return C
}

type Checks = []Check
type Check struct {
	// Type of check; http, https, syn, dns
	Type string `json:"type,omitempty"`

	// TCP/UDP port to use for L4/L7 checks
	Port uint16 `json:"port,omitempty"`

	// HTTP Host header to send in healthcheck
	Host string `json:"host,omitempty"`

	// Path of resource to use when building a URI for HTTP/HTTPS healthchecks
	Path string `json:"path,omitempty"`

	// Expected HTTP status codes to allow check to succeed
	Expect []int `json:"expect,omitempty"`

	// Method - HTTP: GET=false, HEAD=true DNS: UDP=false TCP=true
	Method method `json:"method,omitempty"`
}

func (c *Check) codes() (r string) {
	if len(c.Expect) < 1 {
		return ""
	}

	for i, e := range c.Expect {
		if i == 0 {
			r = fmt.Sprintf("%d", e)
		} else {
			r += fmt.Sprintf(" %d", e)
		}
	}

	return
}

// render checks almost exactly like the raw Go output, but give more
// context to the method field
func (c Check) String() string {

	method := ""

	switch c.Type {
	case "http":
		fallthrough
	case "https":
		if c.Method {
			method = "HEAD"
		} else {
			method = "GET"
		}
	case "dns":
		if c.Method {
			method = "tcp"
		} else {
			method = "udp"
		}
	case "syn":
		method = "tcp"
	}

	return fmt.Sprintf("{%s %d %s %s [%s] %s}", c.Type, c.Port, c.Host, c.Path, c.codes(), method)
}

type method bool

func (m *method) UnmarshalJSON(data []byte) error {

	s := string(data)

	switch s {
	case "true":
		*m = true
		return nil
	case "false":
		*m = false
		return nil
	case `"HEAD"`:
		*m = true
		return nil
	case `"GET"`:
		*m = false
		return nil
	case `"TCP"`:
		*m = true
		return nil
	case `"UDP"`:
		*m = false
		return nil
	case `"tcp"`:
		*m = true
		return nil
	case `"udp"`:
		*m = false
		return nil
	}

	return errors.New("Badly formed method: " + s)
}

func (m *Mon) probes(i Instance, checks Checks, round uint64) (ok bool, s string) {
	for _, c := range checks {

		if c.Port == 0 {
			c.Port = i.Destination.Port
		}

		p := m.Prober

		if p != nil {
			ok, s = p.Probe(m, i, c)
		} else {
			ok, s = m.Probe(i.Destination.Address, c)
		}

		m.check(i, c.String(), round, ok, s)

		if !ok {
			return ok, c.Type + ": " + s
		}
	}

	return true, "OK"
}

func (m *Mon) Probe(addr netip.Addr, c Check) (ok bool, s string) {
	switch c.Type {
	case "http":
		ok, s = m.httpProbe(addr, c.Port, false, bool(c.Method), c.Host, c.Path, c.Expect...)
	case "https":
		ok, s = m.httpProbe(addr, c.Port, true, bool(c.Method), c.Host, c.Path, c.Expect...)
	case "syn":
		ok, s = m.synProbe(addr, c.Port)
	case "dns":
		ok, s = m.dnsProbe(addr, c.Port, bool(c.Method))
	default:
		s = "Unknown check type"
	}

	return
}

// in case the http/s check has no host defined, use the vip as the host portion in the url (for DSR checks)
func (m *Mon) ProbeVIP(vip, addr netip.Addr, c Check) (ok bool, s string) {
	switch c.Type {
	case "http":
		ok, s = m.httpProbeVIP(vip, addr, c.Port, false, bool(c.Method), c.Host, c.Path, c.Expect...)
	case "https":
		ok, s = m.httpProbeVIP(vip, addr, c.Port, true, bool(c.Method), c.Host, c.Path, c.Expect...)
	case "syn":
		ok, s = m.synProbe(addr, c.Port)
	case "dns":
		ok, s = m.dnsProbe(addr, c.Port, bool(c.Method))
	default:
		s = "Unknown check type"
	}

	return
}

func (m *Mon) dnsProbe(addr netip.Addr, port uint16, useTCP bool) (bool, string) {

	if useTCP {
		return dnstcp(addr.String(), port)
	}

	return dnsudp(addr.String(), port)
}

func (m *Mon) synProbe(addr netip.Addr, port uint16) (bool, string) {

	if !addr.Is4() {
		return false, "Not an IPv4 address"
	}

	ip := addr.As4()

	syn := m.syn

	if syn == nil {
		return false, "No SYN server"
	}

	return syn.Check(ip, port)
}

func (m *Mon) httpProbe(addr netip.Addr, port uint16, https bool, head bool, host, path string, expect ...int) (bool, string) {
	return m.httpProbeVIP(netip.Addr{}, addr, port, https, head, host, path, expect...)
}

func ipHost(addr netip.Addr) string {
	if addr.Is6() {
		// https://datatracker.ietf.org/doc/html/rfc6874
		return "[" + strings.Replace(addr.String(), "%", "%25", -1) + "]"
	}
	return addr.String()
}

func (m *Mon) httpProbeVIP(vip, addr netip.Addr, port uint16, https bool, head bool, host, path string, expect ...int) (bool, string) {
	//if m.SNI {
	//	return m.sniHttpProbe(addr, port, https, head, host, path, expect...)
	//}

	//defer client.CloseIdleConnections()

	client := cacheClient(addr)

	if m.CloseIdleConnections {
		defer client.CloseIdleConnections()
	}

	scheme := "http"
	method := "GET"

	if https {
		scheme = "https"
	}

	if head {
		method = "HEAD"
	}

	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	if port == 0 {
		return false, "Port is 0"
	}

	if host == "" {
		if vip.IsValid() {
			host = ipHost(vip)
		} else {
			host = ipHost(addr)
		}
	}

	url := fmt.Sprintf("%s://%s:%d/%s", scheme, host, port, path)

	if !https && port == 80 {
		url = fmt.Sprintf("%s://%s/%s", scheme, host, path)
	}

	if https && port == 443 {
		url = fmt.Sprintf("%s://%s/%s", scheme, host, path)
	}

	req, err := http.NewRequest(method, url, nil)

	if err != nil {
		return false, err.Error()
	}

	resp, err := client.Do(req)

	if err != nil {
		return false, err.Error()
	}

	defer resp.Body.Close()

	ioutil.ReadAll(resp.Body)

	if len(expect) == 0 {
		expect = []int{200}
	}

	for _, e := range expect {
		if e == 0 || resp.StatusCode == e {
			return true, resp.Status
		}
	}

	return false, method + " " + url + " - " + resp.Status
}

// Actually, turns out that the below is needed for Microsoft ADFS probes:
// https://pdfs.loadbalancer.org/Microsoft_ADFS_Deployment_Guide.pdf
// Only on HTTP: /adfs/probe will return 200
// Only on HTTPS; /adfs/ls will return 200
// new code below the comments

// unlikely, but may need to override for SNI in case remote server selects handler based on TLS values?
// something like: https://github.com/golang/go/issues/22704
/*
dialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
	DualStack: true,
}

client := http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// redirect all connections to 127.0.0.1
			addr = "127.0.0.1" + addr[strings.LastIndex(addr, ":"):]
			return dialer.DialContext(ctx, network, addr)
		},
	},
}
*/

// create a new client each time, with right IP?

func (m *Mon) sniHttpProbe(addr netip.Addr, port uint16, https bool, head bool, host, path string, expect ...int) (bool, string) {

	// I know that the advice is to reuse clients rather than creating
	// new ones, but the intention here is to check the whole
	// connection process, so cached connections are undesirable and
	// it makes doing SNI to explicit IP addresses hard. The client
	// should be garbage collected anyway, as I understand it.

	sni := sniClient(addr)

	defer sni.CloseIdleConnections()

	scheme := "http"
	method := "GET"

	if https {
		scheme = "https"
	}

	if head {
		method = "HEAD"
	}

	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	if port == 0 {
		return false, "Port is 0"
	}

	if host == "" {
		host = addr.String()

		if addr.Is6() {
			host = "[" + host + "]" // surround IPv6 address with brackets because of colons
		}
	}

	url := fmt.Sprintf("%s://%s:%d/%s", scheme, host, port, path)
	req, err := http.NewRequest(method, url, nil)

	if err != nil {
		return false, err.Error()
	}

	resp, err := sni.Do(req)

	if err != nil {
		return false, err.Error()
	}

	defer resp.Body.Close()

	ioutil.ReadAll(resp.Body)

	if len(expect) == 0 {
		expect = []int{200}
	}

	for _, e := range expect {
		if e == 0 || resp.StatusCode == e {
			return true, resp.Status
		}
	}

	return false, resp.Status
}

func sniClient(host netip.Addr) *http.Client {
	return ipClient(host)
}

// return an http.Client which will always dial the IP address given
// in the argument regardless of the hostname in the URL
func ipClient(host netip.Addr) *http.Client {

	sniHost := func(addr string, ipaddr netip.Addr) string {
		i := strings.LastIndex(addr, ":")
		if i < 0 {
			return addr
		}

		s := ipaddr.String()

		if ipaddr.Is6() {
			s = "[" + s + "]"
		}

		return s + addr[i:]
	}

	return &http.Client{
		Timeout: time.Second * 2,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialer := &net.Dialer{
					Timeout: 2 * time.Second,
				}
				return dialer.DialContext(ctx, network, sniHost(addr, host))
			},
			//Dial:                dialer.Dial,  // "Deprecated: Use DialContext instead"
			TLSHandshakeTimeout: 2 * time.Second,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		},
	}
}

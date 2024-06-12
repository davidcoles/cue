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
	"io"
	"net"
	"sync"
	"time"
)

type pdu []byte

type connection struct {
	C     chan message
	Error string

	closed      chan bool
	writer_exit chan bool
	reader_exit chan bool
	pending     chan bool
	conn        net.Conn
	mutex       sync.Mutex
	out         []pdu
}

func newConnection(local IP4, peer string) (*connection, error) {
	var nul IP4

	dialer := net.Dialer{
		Timeout: 10 * time.Second,
	}

	if local != nul {
		dialer = net.Dialer{
			Timeout: 10 * time.Second,
			LocalAddr: &net.TCPAddr{
				IP:   net.IP(local[:]),
				Port: 0,
			},
		}
	}

	conn, err := dialer.Dial("tcp", peer+":179")

	if err != nil {
		return nil, err
	}

	c := &connection{
		C:           make(chan message),
		closed:      make(chan bool),
		writer_exit: make(chan bool),
		reader_exit: make(chan bool),
		pending:     make(chan bool, 1),
		conn:        conn,
	}

	go c.writer()
	go c.reader()

	return c, nil
}

func (c *connection) local() ([]byte, bool) {

	if a, ok := c.conn.LocalAddr().(*net.TCPAddr); ok {
		return a.IP, true
	}

	return nil, false
}

func (c *connection) close() {
	close(c.closed)
}

func (c *connection) shift() (pdu, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var m pdu

	if len(c.out) < 1 {
		return m, false
	}

	m = c.out[0]
	c.out = c.out[1:]

	select {
	case c.pending <- true: // more messages
	default:
	}

	return m, true
}

func (c *connection) queue(ms ...message) {

	addHeader := func(t byte, d []byte) pdu {
		l := 19 + len(d)
		p := make([]byte, l)
		for n := 0; n < 16; n++ {
			p[n] = 0xff
		}
		hl := htons(uint16(l))
		p[16] = hl[0]
		p[17] = hl[1]
		p[18] = t

		copy(p[19:], d)

		return p
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, m := range ms {
		c.out = append(c.out, addHeader(m.Type(), m.Body()))
	}

	select {
	case c.pending <- true:
	default:
	}
}

func (c *connection) drain() bool {

	for {
		m, ok := c.shift()

		if !ok {
			return true
		}

		c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))

		_, err := c.conn.Write(m)

		if err != nil {
			c.Error = err.Error()
			return false
		}
	}
}

func (c *connection) writer() {
	defer close(c.writer_exit)
	defer c.conn.Close()

	for {
		// if the peer closes the connection then the reader encounters an error and exits (c.reader_exit)
		// if the user asks to close the connection c.close is triggered

		select {
		case <-c.closed:
			c.drain()
			return
		case <-c.reader_exit:
			c.drain()
			return
		case <-c.pending:
			if !c.drain() {
				return
			}
		}
	}
}

func (c *connection) reader() {

	defer close(c.reader_exit)
	defer close(c.C)

	for {
		// try to read a message
		// if the writer side encounders an error, it will exit and close the connction, causing an error here
		// if the user asks to close the connection upstream then writer will exit, closing the net connection (error here)

		var header [19]byte

		n, e := io.ReadFull(c.conn, header[:])
		if n != len(header) || e != nil {
			c.Error = e.Error()
			return
		}

		for _, b := range header[0:16] {
			if b != 0xff {
				return
			}
		}

		length := int(header[16])<<8 + int(header[17])
		mtype := header[18]

		if length < 19 || length > 4096 {
			return
		}

		length -= 19

		body := make([]byte, length)

		n, e = io.ReadFull(c.conn, body[:])
		if n != len(body) || e != nil {
			c.Error = e.Error()
			return
		}

		var m message

		switch mtype {
		case M_OPEN:
			var o open
			o.parse(body) // todo - handle failed parse better (connection gets killed anyway)
			m = &o
		case M_NOTIFICATION:
			var n notification
			n.parse(body) // todo - handle failed parse better (connection gets killed anyway)
			m = &n
		default:
			m = &other{mtype: mtype, body: body}
		}

		select {
		case c.C <- m:
		case <-c.closed: // user wants to close the connection
			c.Error = "Closed"
			return
		case <-c.writer_exit:
			return
		}
	}
}

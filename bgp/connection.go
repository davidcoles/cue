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
	//"fmt"
	"io"
	"net"
	//"regexp"
	"sync"
	"time"
)

type message2 = []byte

type connection struct {
	C     chan message
	Error string

	close       chan bool
	writer_exit chan bool
	reader_exit chan bool
	pending     chan bool
	conn        net.Conn

	mutex sync.Mutex
	out   []message2
}

func addHeader(t byte, d []byte) []byte {
	l := 19 + len(d)
	p := make([]byte, l)
	for n := 0; n < 16; n++ {
		p[n] = 0xff
	}

	p[16] = byte(l >> 8)
	p[17] = byte(l & 0xff)
	p[18] = t

	copy(p[19:], d)

	return p
}

func new_connection2(local IP4, peer string) (*connection, error) {
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
		close:       make(chan bool),
		writer_exit: make(chan bool),
		reader_exit: make(chan bool),
		pending:     make(chan bool, 1),
		conn:        conn,
	}

	go c.writer()
	go c.reader()

	return c, nil
}

func (c *connection) Local() ([]byte, bool) {

	if a, ok := c.conn.LocalAddr().(*net.TCPAddr); ok {
		return a.IP, true
	}

	return nil, false

	/*
		      	addrport := c.conn.LocalAddr().String()
				re4 := regexp.MustCompile(`^(.*):\d+$`)
				re6 := regexp.MustCompile(`^\[(.*?)(|%.*)\]:\d+$`)

				if m := re6.FindStringSubmatch(addrport); len(m) == 3 {
					return net.ParseIP(m[1]).To16(), true
				}

				if m := re4.FindStringSubmatch(addrport); len(m) == 2 {
					return net.ParseIP(m[1]).To4(), true
				}

				return nil, false
	*/
}

func (c *connection) Close() {
	close(c.close)
}

func (c *connection) shift() (message2, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var m message2

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

func (c *connection) write(ms ...message2) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, m := range ms {
		c.out = append(c.out, m)
	}

	select {
	case c.pending <- true:
	default:
	}
}

func (c *connection) queue(t uint8, ms ...message2) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, m := range ms {
		//c.out = append(c.out, headerise(t, m))
		c.out = append(c.out, addHeader(t, m))
	}

	select {
	case c.pending <- true:
	default:
	}
}

func (c *connection) write2(t uint8, m ...byte) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	//c.out = append(c.out, headerise(t, m))
	c.out = append(c.out, addHeader(t, m))

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
		case <-c.close:
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
			var o xopen
			o.parse(body)
			//m = message{mtype: mtype, yopen: newopen(body), xopen: o}
			m = message{mtype: mtype, open: o}
		case M_NOTIFICATION:
			//m = message{mtype: mtype, notification: newnotification(body)}
			var n notification
			n.parse(body)
			m = message{mtype: mtype, notification: n}
		default:
			m = message{mtype: mtype, body: body}
		}

		select {
		case c.C <- m:
		case <-c.close: // user wants to close the connection
			c.Error = "Closed"
			return
		case <-c.writer_exit:
			return
		}
	}
}

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

package log

type Log interface {
	Println(...any)
	EMERG(string, ...interface{})
	ALERT(string, ...interface{})
	CRIT(string, ...interface{})
	ERR(string, ...interface{})
	WARNING(string, ...interface{})
	NOTICE(string, ...interface{})
	INFO(string, ...interface{})
	DEBUG(string, ...interface{})
}

type Nil struct{}

func (n Nil) Println(...any)         {}
func (n Nil) EMERG(string, ...any)   {}
func (n Nil) ALERT(string, ...any)   {}
func (n Nil) CRIT(string, ...any)    {}
func (n Nil) ERR(string, ...any)     {}
func (n Nil) WARNING(string, ...any) {}
func (n Nil) NOTICE(string, ...any)  {}
func (n Nil) INFO(string, ...any)    {}
func (n Nil) DEBUG(string, ...any)   {}

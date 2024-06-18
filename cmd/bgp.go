package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/davidcoles/cue/bgp"
)

/*

  Examples:

  The default next hop value for the address family that the TCP connection is established over is the socket's local address.

  Single address family (IPv4) using local IP address 10.100.200.10 as router ID, to peer router at 10.100.200.254:

  # go run bgp.go 65432 10.100.200.10 10.100.200.254

  Multiprotocol (IPv4 + IPv6) over IPv4, using fe80::210:5aff:feaa:20a2 (a local address) as the IPv6 next hop value:

  # go run bgp.go -m -6 fe80::210:5aff:feaa:20a2 65432 10.100.200.10 10.100.200.254

  Single address family (IPv6) over IPv6, 10.100.200.10 as router ID to peer router at fe80::260:97ff:fe02:6ea5%vlan200:

  # go run bgp.go 65304 10.100.200.10 fe80::260:97ff:fe02:6ea5%vlan200

  Multiprotocol session over IPv6, using the router ID as the IPv4 next hop value:

  # go run bgp.go -m 65304 10.100.200.10 fe80::260:97ff:fe02:6ea5%vlan200

  Multiprotocol session over IPv6, with 10.100.200.10 as the IPv4 next hop value instead of the router ID (10.99.99.99)

  # go run bgp.go -m -4 10.100.200.10 65304 10.99.99.99 fe80::260:97ff:fe02:6ea5%vlan200

*/

func main() {

	var s bgp.Session    // our session object
	var rib []netip.Addr // start with an empty routing information base - it could be pre-populated, though, eg.:
	// rib:= routingInformationBase()

	routerid, peer, parameters := parseCommandLineArguments()

	s.Start(routerid, peer, parameters, rib, &Log{}) // start the session - connections will be retried if they fail initially

	time.Sleep(5 * time.Second)

	rib = routingInformationBase() // populate the RIB
	s.LocRIB(rib)                  // and send the updates

	time.Sleep(60 * time.Second)

	// update parameters - will re-advertise the prefixes
	parameters.MED = 123
	parameters.LocalPref = 123
	s.Configure(parameters)

	time.Sleep(60 * time.Second)

	// dump a JSON representation of the session status
	if js, err := json.MarshalIndent(s.Status(), " ", " "); err != nil {
		log.Fatal(err, js)
	} else {
		fmt.Println(string(js))
	}

	time.Sleep(10 * time.Second)

	s.LocRIB(nil) // withdraw all address by sending an empty RIB

	time.Sleep(5 * time.Second)

	s.Stop()

	time.Sleep(2 * time.Second)
}

func routingInformationBase() (rib []netip.Addr) {

	for n := uint32(0); n < 3; n++ {
		x := uint32(192)<<24 + uint32(168)<<16 + uint32(101)<<8
		ip := htonl(x + n)
		rib = append(rib, netip.AddrFrom4(ip))

		// https://en.wikipedia.org/wiki/Unique_local_address - ULA of fd0b:2b0b:a7b8::/48
		ip6 := [16]byte{0xfd, 0x0b, 0x2b, 0x0b, 0xa7, 0xb8, 0x1e, 0xe7, 0xc0, 0xde, 0x00, 0x00, ip[0], ip[1], ip[2], ip[3]}
		rib = append(rib, netip.AddrFrom16(ip6))
	}

	return
}

func parseCommandLineArguments() ([4]byte, string, bgp.Parameters) {

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <as-number> <router-id> <peer-address>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	multiprotocol := flag.Bool("m", false, "Multiprotocol")
	nexthop6 := flag.String("6", "", "IPv6 next hop")
	nexthop4 := flag.String("4", "", "IPv4 next hop")

	flag.Parse()

	args := flag.Args()

	asnumber, err := strconv.Atoi(args[0])

	if err != nil {
		log.Fatal(err)
	}

	if asnumber < 0 || asnumber > 65535 {
		log.Fatal("Local autonomous system number must be in the range 0-65535")
	}

	routerid := netip.MustParseAddr(args[1]).As4()
	peer := args[2]

	addr := netip.MustParseAddr(peer)

	if addr.Is6() {
		peer = "[" + peer + "]"
		if !addr.IsGlobalUnicast() && addr.Zone() == "" {
			log.Fatal("You must provide a zone for link local addresses")
		}
	}

	parameters := bgp.Parameters{
		ASNumber:      uint16(asnumber),
		Multiprotocol: *multiprotocol,
	}

	if *nexthop6 != "" {
		parameters.NextHop6 = netip.MustParseAddr(*nexthop6).As16()
	}

	if *nexthop4 != "" {
		parameters.NextHop4 = netip.MustParseAddr(*nexthop4).As4()
	}

	any4, err := netip.ParsePrefix("0.0.0.0/0")

	if err != nil {
		log.Fatal(err, any4)
	}

	any6, err := netip.ParsePrefix("::/0")

	if err != nil {
		log.Fatal(err, any6)
	}

	pref4 := "192.168.101.0/24"
	pref6 := "fd0b:2b0b:a7b8::/48"

	prefix4, err := netip.ParsePrefix(pref4)

	if err != nil {
		log.Fatal(err, prefix4)
	}

	prefix6, err := netip.ParsePrefix(pref6)

	if err != nil {
		log.Fatal(err, prefix6)
	}

	// experiment with accept/reject rules:

	//parameters.Accept = append(parameters.Accept, any4)
	//parameters.Accept = append(parameters.Accept, any6)

	//parameters.Reject = append(parameters.Reject, prefix4)
	//parameters.Reject = append(parameters.Reject, prefix6)

	//parameters.Reject = append(parameters.Reject, any6)
	//parameters.Reject = append(parameters.Reject, any4)

	// Dump a summary of the parameters
	if js, err := json.MarshalIndent(parameters, " ", " "); err != nil {
		log.Fatal(err, js)
	} else {
		fmt.Println(string(js))
	}

	return routerid, peer, parameters
}

func htonl(h uint32) [4]byte {
	return [4]byte{byte(h >> 24), byte(h >> 16), byte(h >> 8), byte(h)}
}

type Log struct{}

func (Log) BGPPeer(s string, p bgp.Parameters, b bool) {
	fmt.Println("PEER", s, p, b)
}
func (Log) BGPSession(s string, b bool, x string) {
	fmt.Println("SESSION", s, b, x)
}

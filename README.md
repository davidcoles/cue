# Cue

A library to manage load balanced services. A balancer implementation
uses the `Director` --- configured with a list of services which
include health check definitions --- to monitor backed servers and
notify when state has changed. The balancer can then update the
data-plane to reflect the backend availability status.

A BGP implementation is included and may be used to advertise healthy
virtual IP address to the network. Multiprotocol extensions for IPv6
are now supported.

Primarily this is used in the [vc5](https://github.com/davidcoles/vc5)
load balancer and is subject to some change.

## Documentation

[Documentation is here](https://pkg.go.dev/github.com/davidcoles/cue).

## Command line utilities

A utility for testing the BGP implementation is in the [`cmd/`](cmd/)
directory. This can be used to bring up a session over IPv4 or IPv6
and advertise addresses to a peer with various options for setting
BGP path attributes, filtering exported IP addresses with prefixes.

Example invocations are listed in comments in the code.

## Loopback BGP

I have found that it is possible to connect to a local BIRD instance
and advertise addresses fo re-distribution into the network from
there. Here I connect to BIRD on the loopback interface with 127.0.0.1
as my router ID and the loopback IP addresses for both IPv4 and IPv6:

`go run bgp.go -6 ::1 -m 65001 127.0.0.1 127.0.0.1`

Using th BIRD configuration below I can then connect to a router and
gain the benefits of using BFD. Global IPv6 addresses on the local
server and the router are needed for the IPv6 address to be
re-advertised successfully.

```
log syslog all;
protocol device {}
protocol bfd {}

filter vips {
    if net ~ [ 192.168.101.0/24{32,32} ] then accept; # accept /32 prefixes from 192.168.101.0/24
    if net ~ [ fd0b:2b0b:a7b8:1ee7:c0de::/80{128,128} ] then accept; # similar config for IPv6
    reject;
}

protocol bgp core {
    local    as 65001;
    neighbor as 65000;
    neighbor 10.12.34.56;
    ipv4 { export filter vips; import none; next hop self; };
    ipv6 { export filter vips; import none; next hop self; };
    bfd on;
}

protocol bgp lb {
    local    as 65001;  # iBGP - we could use eBGP if we specify 'multihop':
    neighbor as 65001;  # loopback address doesn't count as "directly connected"
    neighbor 127.0.0.1; # load balancer connects on the loopback interface
    passive;            # load balancer always inititates the connection
    ipv4 { export none; import all; };
    ipv6 { export none;	import all; };
}
```

If you get it working on other implementations then it would be great
to have more sample configurations here.

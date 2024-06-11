# Cue

A library to manage load balanced services. A balancer implementation
is passed to as an interface to the `Director`, and configured with a
list of services which include health check definitions.

Backend servers are added and removed from the balancer's pool by the
`Director` according to the status of health checks.

A BGP implementation is included and may be used to advertise healthy
virtual IP address to the network.

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

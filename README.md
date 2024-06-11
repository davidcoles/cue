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

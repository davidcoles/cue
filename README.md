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

[Documentation is here](https://pkg.go.dev/github.com/davidcoles/xvs).

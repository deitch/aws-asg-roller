package main

type readiness interface {
	getUnreadyCount(hostnames []string, ids []string) (int, error)
	prepareTermination(hostnames []string, ids []string, drain, drainForce bool) error
}

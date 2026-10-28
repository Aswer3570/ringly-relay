//go:build !linux

package netcheck

// EstablishedOn — the connection counter exists only on Linux (/proc/net/tcp).
func EstablishedOn(port int) (int, bool) { return 0, false }

func listeningInKernel(port int) (bool, bool) { return false, false }

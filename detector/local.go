package detector

import (
	"context"
	"net"
	"time"
)

// Local address checks and scheduling
// =============================

func pause(ctx context.Context, duration time.Duration) bool {

	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func localIP(ip string) (bool, error) {
	return localIPUsing(ip, net.InterfaceAddrs)
}

func localIPUsing(ip string, list func() ([]net.Addr, error)) (bool, error) {

	addresses, err := list()
	if err != nil {
		return false, err
	}

	for _, address := range addresses {
		addressIP, _, err := net.ParseCIDR(address.String())
		if err == nil && addressIP.Equal(net.ParseIP(ip)) {
			return true, nil
		}
	}

	return false, nil
}

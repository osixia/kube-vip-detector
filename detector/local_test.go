package detector

import (
	"fmt"
	"net"
	"testing"
)

func TestLocalProbeGuard(t *testing.T) {
	addresses := func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("203.0.113.10"), Mask: net.CIDRMask(32, 32)}}, nil
	}
	local, err := localIPUsing("203.0.113.10", addresses)
	if err != nil || !local {
		t.Fatalf("%v %v", local, err)
	}
	local, err = localIPUsing("198.51.100.20", addresses)
	if err != nil || local {
		t.Fatalf("%v %v", local, err)
	}
	_, err = localIPUsing("203.0.113.10", func() ([]net.Addr, error) { return nil, fmt.Errorf("cannot inspect interfaces") })
	if err == nil {
		t.Fatal("interface inspection failure ignored")
	}
}

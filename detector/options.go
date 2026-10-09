package detector

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Detector options
// =============================

// Options configures VIP and peer probes and node label updates. New validates
// settings for configured targets without applying defaults.
type Options struct {
	WatchCRDs        bool          `mapstructure:"-"`
	Peers            []string      `mapstructure:"peers"`
	PeerLabelPrefix  string        `mapstructure:"peer-label-prefix"`
	VIPs             []string      `mapstructure:"vips"`
	Key              string        `mapstructure:"key"`
	VIPLabelPrefix   string        `mapstructure:"vip-label-prefix"`
	Port             int           `mapstructure:"port"`
	SuccessThreshold int           `mapstructure:"success-threshold"`
	FailureThreshold int           `mapstructure:"failure-threshold"`
	Interval         time.Duration `mapstructure:"interval"`
	Timeout          time.Duration `mapstructure:"timeout"`
	DryRun           bool          `mapstructure:"dry-run"`
}

// Validate checks the probe and label configuration.
func (o Options) Validate() error {

	seen := map[string]bool{}
	for _, ip := range o.VIPs {
		addr, err := netip.ParseAddr(ip)
		if err != nil || addr.Is4In6() || addr.Zone() != "" || addr.IsLinkLocalUnicast() || addr.String() != ip || addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() {
			return fmt.Errorf("--vips: invalid canonical unicast IPv4 or IPv6: %q", ip)
		}

		if seen[ip] {
			return fmt.Errorf("--vips: duplicate VIP %s", ip)
		}

		seen[ip] = true
	}

	if err := o.validatePeers(); err != nil {
		return err
	}
	if len(o.VIPs) == 0 && len(o.Peers) == 0 && !o.WatchCRDs {
		return errors.New("at least one VIP or peer is required")
	}
	if o.Interval <= 0 || o.Timeout <= 0 || o.SuccessThreshold < 1 || o.FailureThreshold < 1 {
		return errors.New("durations must be positive and thresholds must be at least 1")
	}
	if len(o.VIPs) == 0 && !o.WatchCRDs {
		return nil
	}
	if o.Key == "" {
		return errors.New("HMAC key is required")
	}

	if o.Port < 1024 || o.Port > 65535 || o.Interval <= 0 || o.Timeout <= 0 || o.SuccessThreshold < 1 || o.FailureThreshold < 1 {
		return errors.New("invalid flags: port must be 1024–65535, durations must be positive, and thresholds must be at least 1")
	}

	// Validate the actual labels, including the encoded IPv6 suffix.
	if o.VIPLabelPrefix == "" {
		return errors.New("--vip-label-prefix must not be empty")
	}
	if o.WatchCRDs {
		for _, key := range []string{vipLabel(o.VIPLabelPrefix, "2001:db8::1"), o.PeerLabelPrefix + "ns.peer"} {
			if problems := validation.IsQualifiedName(key); len(problems) != 0 {
				return fmt.Errorf("invalid CRD label prefix: %s", strings.Join(problems, "; "))
			}
		}
		if o.PeerLabelPrefix == "" {
			return errors.New("--peer-label-prefix must not be empty")
		}
	}

	for _, ip := range o.VIPs {
		if problems := validation.IsQualifiedName(vipLabel(o.VIPLabelPrefix, ip)); len(problems) != 0 {
			return fmt.Errorf("invalid --vip-label-prefix for VIP %q: %s", ip, strings.Join(problems, "; "))
		}
	}

	key, err := hex.DecodeString(o.Key)
	if err != nil || len(key) != 32 {
		return errors.New("HMAC key must contain exactly 64 hexadecimal characters (32 random bytes)")
	}

	return nil
}

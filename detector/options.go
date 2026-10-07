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

// Options configures VIP probes and node label updates. All fields must be set
// explicitly; New validates them without applying defaults.
type Options struct {
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
		if err != nil || !addr.Is4() || addr.String() != ip || addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() {
			return fmt.Errorf("--vips: invalid canonical unicast IPv4: %q", ip)
		}

		if seen[ip] {
			return fmt.Errorf("--vips: duplicate VIP %s", ip)
		}

		seen[ip] = true
	}

	if o.Key == "" {
		return errors.New("HMAC key is required")
	}

	if o.Port < 1024 || o.Port > 65535 || o.Interval <= 0 || o.Timeout <= 0 || o.SuccessThreshold < 1 || o.FailureThreshold < 1 {
		return errors.New("invalid flags: port must be 1024–65535, durations must be positive, and thresholds must be at least 1")
	}

	// Prefix is literal: domain.example/vip- produces domain.example/vip-<IPv4>.
	if o.VIPLabelPrefix == "" {
		return errors.New("--vip-label-prefix must not be empty")
	}

	if problems := validation.IsQualifiedName(o.VIPLabelPrefix + "255.255.255.255"); len(problems) != 0 {
		return fmt.Errorf("invalid --vip-label-prefix: %s", strings.Join(problems, "; "))
	}

	key, err := hex.DecodeString(o.Key)
	if err != nil || len(key) != 32 {
		return errors.New("HMAC key must contain exactly 64 hexadecimal characters (32 random bytes)")
	}

	return nil
}

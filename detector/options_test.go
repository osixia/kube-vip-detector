package detector

import (
	"strings"
	"testing"
	"time"

	"github.com/osixia/kube-network-detector/config"
)

func validOptions() Options {
	return Options{VIPs: []string{"203.0.113.10"}, Key: strings.Repeat("a", 64), VIPLabelPrefix: config.DefaultVIPLabelPrefix, Port: 9876, Interval: time.Second, Timeout: time.Second, SuccessThreshold: 2, FailureThreshold: 3}
}

func TestOptionsValidateKey(t *testing.T) {
	for _, value := range []string{strings.Repeat("a", 64), strings.Repeat("A", 64)} {
		options := validOptions()
		options.Key = value
		if err := options.Validate(); err != nil {
			t.Fatal("valid key was not decoded correctly")
		}
	}
}

func TestOptionsValidateKeyRejectsInvalidValuesWithoutDisclosure(t *testing.T) {
	for _, value := range []string{"", "invalid-secret", strings.Repeat("a", 62), strings.Repeat("a", 66), strings.Repeat("z", 64)} {
		options := validOptions()
		options.Key = value
		err := options.Validate()
		if err == nil {
			t.Fatal("invalid key accepted")
		}
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("key value disclosed in error")
		}
	}
}

func TestOptionsValidateVIPs(t *testing.T) {
	for _, x := range []struct {
		value []string
		valid bool
	}{
		{[]string{"198.51.100.20", "203.0.113.10"}, true}, {nil, false}, {[]string{}, false},
		{[]string{"203.0.113.10", "203.0.113.10"}, false}, {[]string{"127.0.0.1"}, false},
		{[]string{"::1"}, false}, {[]string{"0.0.0.0"}, false}, {[]string{"224.0.0.1"}, false},
		{[]string{""}, false}, {[]string{"bad-ip"}, false},
		{[]string{"203.0.113.010"}, false}, {[]string{" 203.0.113.10"}, false},
		{[]string{"203.0.113.10,203.0.113.11"}, false},
	} {
		options := validOptions()
		options.VIPs = x.value
		err := options.Validate()
		if (err == nil) != x.valid {
			t.Errorf("%q: %v", x.value, err)
		}
	}
}

func TestOptionsValidateLabelPrefix(t *testing.T) {
	for _, x := range []struct {
		prefix string
		valid  bool
	}{
		{config.DefaultVIPLabelPrefix, true}, {"network.example.net/vip-", true}, {"vip-", true},
		{"", false}, {"invalid domain/", false}, {"example.net/a/b-", false},
	} {
		options := validOptions()
		options.VIPLabelPrefix = x.prefix
		if err := options.Validate(); (err == nil) != x.valid {
			t.Errorf("%q: %v", x.prefix, err)
		}
	}
}

func TestOptionsValidateProbeSettings(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Options)
	}{
		{"privileged port", func(o *Options) { o.Port = 1023 }},
		{"port above range", func(o *Options) { o.Port = 65536 }},
		{"zero interval", func(o *Options) { o.Interval = 0 }},
		{"negative interval", func(o *Options) { o.Interval = -time.Second }},
		{"zero timeout", func(o *Options) { o.Timeout = 0 }},
		{"negative timeout", func(o *Options) { o.Timeout = -time.Second }},
		{"zero success threshold", func(o *Options) { o.SuccessThreshold = 0 }},
		{"negative success threshold", func(o *Options) { o.SuccessThreshold = -1 }},
		{"zero failure threshold", func(o *Options) { o.FailureThreshold = 0 }},
		{"negative failure threshold", func(o *Options) { o.FailureThreshold = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			test.change(&options)
			if err := options.Validate(); err == nil {
				t.Fatal("invalid probe settings accepted")
			}
		})
	}

	for _, port := range []int{1024, 65535} {
		options := validOptions()
		options.Port = port
		options.SuccessThreshold = 1
		options.FailureThreshold = 1
		if err := options.Validate(); err != nil {
			t.Fatalf("valid boundary settings rejected: %v", err)
		}
	}
}

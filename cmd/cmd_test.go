package cmd

import (
	"testing"

	"github.com/osixia/kube-network-detector/config"
)

func TestCRDDiscoveryDefaultAndDisableFlag(t *testing.T) {
	flag := cmd.Flags().Lookup("disable-crds")
	if flag == nil || flag.NoOptDefVal != "true" || cmd.Flags().Lookup("watch-crds") != nil {
		t.Fatal("expected a single bare --disable-crds boolean flag")
	}
	saved := *cmdFlags
	savedValue, savedChanged := flag.Value.String(), flag.Changed
	t.Cleanup(func() {
		*cmdFlags = saved
		_ = flag.Value.Set(savedValue)
		flag.Changed = savedChanged
	})
	for _, test := range []struct {
		name  string
		env   string
		args  []string
		watch bool
	}{
		{name: "enabled by default", watch: true},
		{name: "bare flag disables", args: []string{"--disable-crds"}},
		{name: "environment disables", env: "true"},
		{name: "explicit false overrides environment", env: "true", args: []string{"--disable-crds=false"}, watch: true},
		{name: "bare flag overrides environment", env: "false", args: []string{"--disable-crds"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(config.EnvironmentPrefix+"_DISABLE_CRDS", test.env)
			if err := flag.Value.Set("false"); err != nil {
				t.Fatal(err)
			}
			flag.Changed = false
			if err := cmd.ParseFlags(test.args); err != nil {
				t.Fatal(err)
			}
			if err := cmd.PersistentPreRunE(cmd, nil); err != nil {
				t.Fatal(err)
			}
			if cmdFlags.WatchCRDs != test.watch {
				t.Fatalf("WatchCRDs=%t, want %t", cmdFlags.WatchCRDs, test.watch)
			}
			// Static-only peer operation must still work without VIP credentials.
			if !test.watch {
				o := cmdFlags.Options
				o.Peers = []string{"db=10.0.0.20:5432"}
				if err := o.Validate(); err != nil {
					t.Fatal("static-only peer configuration rejected:", err)
				}
			}
		})
	}
}

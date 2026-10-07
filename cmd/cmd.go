package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	cmdlog "github.com/osixia/container-baseimage/cmd/log"
	"github.com/osixia/container-baseimage/helpers"
	"github.com/osixia/container-baseimage/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/osixia/kube-vip-detector/config"
	"github.com/osixia/kube-vip-detector/detector"
)

type flags struct {
	detector.Options
}

var cmdFlags = &flags{}

var cnf = viper.New()

var cmd = &cobra.Command{
	Use: "kube-vip-detector",

	Short:   "Detect which node receives an IPv4 VIP and maintain Kubernetes node labels",
	Long:    "Detect which node receives an IPv4 VIP and maintain Kubernetes node labels.\nAll flags also accept " + config.EnvironmentPrefix + "_ environment variables (replace hyphens with underscores).\nExplicit flags override environment variables; environment variables override defaults.",
	Example: "  kube-vip-detector --vips=203.0.113.10,198.51.100.20 --key=<64-hex-character-key>",

	Args: cobra.NoArgs,

	Version: config.Image(),

	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {

		helpers.Mustf(cnf.Unmarshal(&cmdFlags.Options), "Invalid detector configuration")

		return cmdlog.HandleFlags(cmd)
	},

	RunE: func(cmd *cobra.Command, args []string) error {
		log.Tracef("Run: %v", cmd.Use)

		return runDetector(cmd.Context(), cmdFlags.Options)
	},
}

func init() {

	cnf.SetEnvPrefix(config.EnvironmentPrefix)
	cnf.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	cnf.AllowEmptyEnv(true)
	cnf.AutomaticEnv()

	// cmd options
	cmd.SetVersionTemplate("{{.Version}}\n")

	// flags
	cmd.Flags().SortFlags = false

	cmd.Flags().StringSliceVarP(&cmdFlags.VIPs, "vips", "", nil, "comma-separated canonical IPv4 VIPs; flag may be repeated, omit for responder-only mode")
	cmd.Flags().StringVar(&cmdFlags.VIPLabelPrefix, "vip-label-prefix", config.DefaultVIPLabelPrefix, "literal prefix prepended to each VIP IPv4 label")

	cmd.Flags().StringVar(&cmdFlags.Key, "key", "", "hex-encoded 32-byte HMAC key (64 hexadecimal characters)")
	cmd.Flags().IntVar(&cmdFlags.Port, "port", 9876, "direct node TCP probe port")

	cmd.Flags().DurationVar(&cmdFlags.Interval, "interval", 5*time.Second, "delay between probes and local-address checks")
	cmd.Flags().DurationVar(&cmdFlags.Timeout, "timeout", 2*time.Second, "probe timeout")
	cmd.Flags().IntVar(&cmdFlags.SuccessThreshold, "success-threshold", 2, "consecutive identical successes before labeling")
	cmd.Flags().IntVar(&cmdFlags.FailureThreshold, "failure-threshold", 3, "consecutive failures before removing labels")

	cmdlog.AddFlags(cmd.Flags())

	cmd.Flags().BoolVar(&cmdFlags.DryRun, "dry-run", false, "probe and report proposed changes; never write Nodes or Leases")

	helpers.Must(cnf.BindPFlags(cmd.Flags()))
}

func Run(ctx context.Context) error {
	return cmd.ExecuteContext(ctx)
}

func runDetector(ctx context.Context, options detector.Options) error {

	identity := detector.Identity{
		Node:      os.Getenv(config.EnvironmentPrefix + "_NODE_NAME"),
		Namespace: os.Getenv(config.EnvironmentPrefix + "_POD_NAMESPACE"),
		PodUID:    os.Getenv(config.EnvironmentPrefix + "_POD_UID"),
	}
	if identity.Node == "" || identity.Namespace == "" || identity.PodUID == "" {
		return fmt.Errorf("%[1]s_NODE_NAME, %[1]s_POD_NAMESPACE and %[1]s_POD_UID are required (Downward API)", config.EnvironmentPrefix)
	}
	if err := options.Validate(); err != nil {
		return err
	}

	clientConfig, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	clientConfig.Timeout = 5 * time.Second
	client, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return err
	}

	service, err := detector.New(client, identity, options)
	if err != nil {
		return err
	}
	return service.Run(ctx)
}

package main

import (
	"flag"
	"os"

	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/alertreceiver"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(ranv1alpha1.AddToScheme(scheme))
}

func main() {
	var bindAddress string

	opts := ctrlzap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.StringVar(&bindAddress, "bind-address", ":8080", "Address and port for the webhook HTTP server.")
	flag.Parse()

	// BindFlags wires --zap-log-level to opts.Level when parsed.
	// Initialize AtomicLevel to match so the level is mutable at runtime.
	initialLevel := zapcore.InfoLevel
	if opts.Level != nil && opts.Level.Enabled(zapcore.DebugLevel) {
		initialLevel = zapcore.DebugLevel
	}
	atomicLevel := uberzap.NewAtomicLevelAt(initialLevel)

	ctrl.SetLogger(ctrlzap.New(
		ctrlzap.UseFlagOptions(&opts),
		func(o *ctrlzap.Options) { o.Level = atomicLevel },
	))
	logger := ctrl.Log.WithName("alert-receiver")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		logger.Error(err, "unable to get cluster config")
		os.Exit(1)
	}

	// Use a plain (non-caching) client so the receiver can read resources on demand
	// without needing to run a full controller manager.
	hubClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "unable to build hub client")
		os.Exit(1)
	}

	srv := alertreceiver.NewServer(bindAddress, hubClient, &atomicLevel)
	logger.Info("starting alert receiver", "address", bindAddress)

	ctx := ctrl.SetupSignalHandler()
	if err := srv.Start(ctx); err != nil {
		logger.Error(err, "alert receiver exited with error")
		os.Exit(1)
	}
}

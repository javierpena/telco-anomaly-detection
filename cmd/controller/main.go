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
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(ranv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		operatorNamespace    string
		alertReceiverURL     string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address for the metrics endpoint.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address for health probe endpoints.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for HA deployments.")
	flag.StringVar(&operatorNamespace, "operator-namespace", "telco-healthcheck-system",
		"Namespace where the operator is deployed; used to construct the alert receiver service URL.")
	flag.StringVar(&alertReceiverURL, "alert-receiver-url", "",
		"Override the alert receiver webhook URL. Defaults to the in-cluster service URL.")

	opts := ctrlzap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
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
	logger := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "telco-anomaly-detection-leader.ran.openshift.io",
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err = (&controller.TelcoHealthcheckReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		OperatorNamespace:   operatorNamespace,
		AlertReceiverSvcURL: alertReceiverURL,
		LogLevel:            &atomicLevel,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create TelcoHealthcheck controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	logger.Info("starting telco-anomaly-detection controller",
		"operatorNamespace", operatorNamespace,
		"leaderElection", enableLeaderElection)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

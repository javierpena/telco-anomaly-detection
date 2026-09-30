package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

const metricsReadTimeout = 5 * time.Second

// MetricsCollector reports the current state of hub-side health checks.
// Each scrape reads the API so removed phases do not leave stale series behind.
type MetricsCollector struct {
	reader    client.Reader
	namespace string
	clusters  *prometheus.Desc
	runs      *prometheus.Desc
	action    *prometheus.Desc
	phase     *prometheus.Desc
}

func NewMetricsCollector(reader client.Reader, namespace string) *MetricsCollector {
	return &MetricsCollector{
		reader:    reader,
		namespace: namespace,
		clusters: prometheus.NewDesc("telco_healthcheck_managed_clusters",
			"Number of clusters monitored by the TelcoHealthcheck singleton.", nil, nil),
		runs: prometheus.NewDesc("telco_healthcheck_runs",
			"Number of hub-side TelcoHealthCheckRun records in the operator namespace.", nil, nil),
		action: prometheus.NewDesc("telco_healthcheck_runs_action_required",
			"Number of hub-side TelcoHealthCheckRun records requiring action.", nil, nil),
		phase: prometheus.NewDesc("telco_healthcheck_runs_by_phase",
			"Number of hub-side TelcoHealthCheckRun records by current phase.", []string{"phase"}, nil),
	}
}

func (c *MetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.clusters
	ch <- c.runs
	ch <- c.action
	ch <- c.phase
}

func (c *MetricsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsReadTimeout)
	defer cancel()

	thc := &ranv1alpha1.TelcoHealthcheck{}
	clusterCount := 0
	if err := c.reader.Get(ctx, types.NamespacedName{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}, thc); err != nil {
		if !apierrors.IsNotFound(err) {
			ch <- prometheus.NewInvalidMetric(c.clusters, fmt.Errorf("reading TelcoHealthcheck: %w", err))
			return
		}
	} else {
		clusterCount = len(thc.Status.MonitoredClusters)
	}

	records := &ranv1alpha1.TelcoHealthCheckRunList{}
	if err := c.reader.List(ctx, records, client.InNamespace(c.namespace)); err != nil {
		ch <- prometheus.NewInvalidMetric(c.runs, fmt.Errorf("listing TelcoHealthCheckRuns: %w", err))
		return
	}

	phaseCounts := make(map[string]int)
	actionRequired := 0
	for _, record := range records.Items {
		if record.Status.AgenticRunActionRequired == "True" {
			actionRequired++
		}
		phase := "Unknown"
		if record.Status.AgenticRunStatus != nil && record.Status.AgenticRunStatus.Phase != "" {
			phase = record.Status.AgenticRunStatus.Phase
		}
		phaseCounts[phase]++
	}

	ch <- prometheus.MustNewConstMetric(c.clusters, prometheus.GaugeValue, float64(clusterCount))
	ch <- prometheus.MustNewConstMetric(c.runs, prometheus.GaugeValue, float64(len(records.Items)))
	ch <- prometheus.MustNewConstMetric(c.action, prometheus.GaugeValue, float64(actionRequired))
	for phase, count := range phaseCounts {
		ch <- prometheus.MustNewConstMetric(c.phase, prometheus.GaugeValue, float64(count), phase)
	}
}

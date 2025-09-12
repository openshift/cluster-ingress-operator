package operator

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	ctrlruntimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// StartMetricsListener starts the metrics listener on addr.
func StartMetricsListener(addr string, signal context.Context) {
	// These metrics get registered in controller-runtime's registry via an init in the internal/controller/metrics package.
	// Unregister the controller-runtime metrics, so that we can combine the controller-runtime metric's registry
	// with that of the ingress-operator. This shouldn't have any side effects, as long as no 2 metrics across
	// controller runtime or the ingress operator share the same name (which is unlikely). See
	// https://github.com/kubernetes/test-infra/blob/master/prow/metrics/metrics.go for additional context.
	ctrlruntimemetrics.Registry.Unregister(collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll)))
	ctrlruntimemetrics.Registry.Unregister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Create prometheus handler by combining the ingress-operator registry
	// with the ingress-operator's controller runtime metrics registry.
	handler := promhttp.HandlerFor(
		prometheus.Gatherers{prometheus.DefaultGatherer, ctrlruntimemetrics.Registry},
		promhttp.HandlerOpts{},
	)

	log.Info("starting metrics listener", "addr", addr)
	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)
	s := http.Server{Addr: addr, Handler: mux}

	go func() {
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "metrics listener exited")
		}
	}()
	<-signal.Done()
	if err := s.Shutdown(context.Background()); err != http.ErrServerClosed {
		log.Error(err, "error stopping metrics listener")
	}
}

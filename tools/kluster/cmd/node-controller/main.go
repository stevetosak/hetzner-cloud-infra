// Command node-controller runs in the cluster and removes a dead Worker
// (ADR 0016, Amendment 2026-11-24): after 20 minutes not Ready it gives the
// Node the out-of-service taint and then deletes it, held back by the guards
// of internal/deadnode. Its manifests are in core/nodes.
//
// In the cluster it uses its ServiceAccount; --kubeconfig points it at
// another cluster, as the k3d test does.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/deadnode"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodecontroller"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("node-controller stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := deadnode.Defaults
	kubeconfig := flag.String("kubeconfig", "", "a kubeconfig file; empty uses the pod's ServiceAccount")
	flag.DurationVar(&cfg.After, "after", cfg.After, "how long a Worker stays not Ready before it is due")
	flag.DurationVar(&cfg.DrainWait, "drain-wait", cfg.DrainWait, "how long a tainted Worker's pods and volumes get to go before its delete")
	interval := flag.Duration("interval", 30*time.Second, "the time between two passes")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	listen := flag.String("listen", ":"+port, "the address of /metrics, /health/live and /health")
	healthCheck := flag.Bool("health-check", false, "ask a running node-controller on this host for /health/live and exit; the image's HEALTHCHECK, which has no shell or curl")
	flag.Parse()
	if *healthCheck {
		return probeLive(port)
	}

	rc, err := restConfig(*kubeconfig)
	if err != nil {
		return err
	}
	rc.UserAgent = "node-controller"
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	factory := informers.NewSharedInformerFactory(client, 0)
	nodes := factory.Core().V1().Nodes()
	attachments := factory.Storage().V1().VolumeAttachments()
	nodeInformer, vaInformer := nodes.Informer(), attachments.Informer()

	broadcaster := record.NewBroadcaster(record.WithContext(ctx))
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: client.CoreV1().Events("")})
	defer broadcaster.Shutdown()
	recorder := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "node-controller"})

	metrics := nodecontroller.NewMetrics()
	c := nodecontroller.New(client, nodes.Lister(), attachments.Lister(), recorder, metrics, cfg, log)

	var synced atomic.Bool
	srv := &http.Server{
		Addr:              *listen,
		Handler:           nodecontroller.Handler(metrics, synced.Load, c.LastPass, 3*(*interval)),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), nodeInformer.HasSynced, vaInformer.HasSynced) {
		return errors.New("the Node and VolumeAttachment caches did not sync")
	}
	synced.Store(true)
	log.Info("started", "after", cfg.After, "drainWait", cfg.DrainWait, "interval", *interval, "listen", *listen)

	done := make(chan struct{})
	go func() { c.Run(ctx, *interval); close(done) }()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stop()
		<-done
		return fmt.Errorf("the HTTP server stopped: %w", err)
	}
	<-done
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// probeLive asks the node-controller on this host for /health/live.
func probeLive(port string) error {
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://127.0.0.1:" + port + "/health/live")
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("/health/live answered %s", res.Status)
	}
	return nil
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

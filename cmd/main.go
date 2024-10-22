/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"os"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/argoproj-labs/gitops-promoter/internal/utils"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/controller"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(promoterv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var promotionStrategyRequeue string
	var proposedCommitRequeue string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":9080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":9081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&promotionStrategyRequeue, "promotion-strategy-requeue-duration", "60s",
		"How frequently to requeue promotion strategy resources for auto reconciliation")
	flag.StringVar(&proposedCommitRequeue, "proposed-commit-requeue-duration", "60s",
		"How frequently to requeue proposed commit resources for auto reconciliation")
	opts := zap.Options{
		Development: true,
		TimeEncoder: zapcore.ISO8601TimeEncoder,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "b21a50c7.argoproj.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil || mgr == nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	//TODO: Create secret informer, and possibly ScmProvider Informer to pass into controllers
	//kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	//informerFactory := informers.NewSharedInformerFactory(kubeClient, 10*time.Minute)

	pathLookup := utils.NewPathLookup()

	if err = (&controller.PullRequestReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("PullRequest"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PullRequest")
		os.Exit(1)
	}
	if err = (&controller.CommitStatusReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("CommitStatus"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CommitStatus")
		os.Exit(1)
	}
	if err = (&controller.RevertCommitReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("RevertCommit"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RevertCommit")
		os.Exit(1)
	}

	proposedCommitRequeueDuration, err := time.ParseDuration(proposedCommitRequeue)
	if err != nil {
		setupLog.Error(err, "failed to parse proposed commit requeue duration")
		os.Exit(1)
	}
	if err = (&controller.ProposedCommitReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		PathLookup: pathLookup,
		Recorder:   mgr.GetEventRecorderFor("ProposedCommit"),
		Config: controller.ProposedCommitReconcilerConfig{
			RequeueDuration: proposedCommitRequeueDuration,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ProposedCommit")
		os.Exit(1)
	}

	promotionStrategyRequeueDuration, err := time.ParseDuration(promotionStrategyRequeue)
	if err != nil {
		setupLog.Error(err, "failed to parse promotion strategy requeue duration")
		os.Exit(1)
	}
	if err = (&controller.PromotionStrategyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("PromotionStrategy"),
		Config: controller.PromotionStrategyReconcilerConfig{
			RequeueDuration: promotionStrategyRequeueDuration,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PromotionStrategy")
		os.Exit(1)
	}
	if err = (&controller.ScmProviderReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("ScmProvider"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ScmProvider")
		os.Exit(1)
	}
	if err = (&controller.GitRepositoryReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitRepository")
		os.Exit(1)
	}
	if err = (&controller.ChangeTransferPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ChangeTransferPolicy")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	processSignals := ctrl.SetupSignalHandler()

	//setupLog.Info("starting informer factory")
	//informerFactory.Start(processSignals.Done())
	//informerFactory.WaitForCacheSync(processSignals.Done())

	setupLog.Info("starting manager")
	if err := mgr.Start(processSignals); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
	setupLog.Info("Cleaning up cloned directories")

	for _, path := range pathLookup.GetAll() {
		err := os.RemoveAll(path)
		if err != nil {
			setupLog.Error(err, "failed to cleanup directory")
		}
		setupLog.Info("cleaning directory", "directory", path)
	}
}

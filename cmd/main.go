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
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"syscall"

	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"github.com/argoproj-labs/gitops-promoter/internal/aggregationapi/apiserver"
	"github.com/argoproj-labs/gitops-promoter/internal/controller"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"github.com/argoproj-labs/gitops-promoter/internal/webserver"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils/gitpaths"
	"github.com/argoproj-labs/gitops-promoter/internal/webhookreceiver"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	"k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/options"
	"k8s.io/apiserver/pkg/util/compatibility"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/common"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	kubeconfigprovider "sigs.k8s.io/multicluster-runtime/providers/kubeconfig"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = utils.GetScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func newControllerCommand(clientConfig clientcmd.ClientConfig) *cobra.Command {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var pprofAddr string

	cmd := &cobra.Command{
		Use:   "controller",
		Short: "GitOps Promoter controller",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runController(
				metricsAddr,
				probeAddr,
				pprofAddr,
				enableLeaderElection,
				secureMetrics,
				enableHTTP2,
				clientConfig,
			)
		},
	}

	cmd.Flags().StringVar(&metricsAddr, "metrics-bind-address", ":9080", "The address the metric endpoint binds to.")
	cmd.Flags().StringVar(&probeAddr, "health-probe-bind-address", ":9081", "The address the probe endpoint binds to.")
	cmd.Flags().StringVar(&pprofAddr, "pprof-bind-address", "",
		"The address the pprof endpoint binds to. If unset, pprof is disabled.")
	cmd.Flags().BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	cmd.Flags().BoolVar(&secureMetrics, "metrics-secure", false, "If set the metrics endpoint is served securely")
	cmd.Flags().BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	return cmd
}

func runController(
	metricsAddr string,
	probeAddr string,
	pprofAddr string,
	enableLeaderElection bool,
	secureMetrics bool,
	enableHTTP2 bool,
	clientConfig clientcmd.ClientConfig,
) error {
	controllerNamespace, _, err := clientConfig.Namespace()
	if err != nil {
		setupLog.Error(err, "failed to get namespace")
		os.Exit(1)
	}

	// Recover any panic and log using the configured logger. This ensures that panics get logged in JSON format if
	// JSON logging is enabled.
	defer func() {
		if r := recover(); r != nil {
			setupLog.Error(nil, "recovered from panic", "panic", r, "trace", string(debug.Stack()))
			os.Exit(1)
		}
	}()

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

	// Create the kubeconfig provider with options
	providerOpts := kubeconfigprovider.Options{
		Namespace:             controllerNamespace,
		KubeconfigSecretLabel: constants.KubeconfigSecretLabel,
		KubeconfigSecretKey:   constants.KubeconfigSecretKey,
		ClusterOptions: []cluster.Option{
			func(clusterOptions *cluster.Options) {
				clusterOptions.Scheme = scheme
			},
		},
	}

	// Create the provider first, then the manager with the provider
	provider := kubeconfigprovider.New(providerOpts)

	mcMgr, err := mcmanager.New(ctrl.GetConfigOrDie(), provider, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		PprofBindAddress:       pprofAddr,
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
	if err != nil {
		panic(fmt.Errorf("unable to start manager: %w", err))
	}
	if mcMgr == nil {
		panic("unable to start manager: mcMgr is nil")
	}

	localManager := mcMgr.GetLocalManager()

	settingsMgr := settings.NewManager(localManager.GetClient(), localManager.GetAPIReader(), settings.ManagerConfig{
		ControllerNamespace: controllerNamespace,
	})

	processSignalsCtx := ctrl.SetupSignalHandler()

	if err = (&controller.PullRequestReconciler{
		Client:      localManager.GetClient(),
		Scheme:      localManager.GetScheme(),
		Recorder:    localManager.GetEventRecorderFor("PullRequest"),
		SettingsMgr: settingsMgr,
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create PullRequest controller: %w", err))
	}
	if err = (&controller.RevertCommitReconciler{
		Client:   localManager.GetClient(),
		Scheme:   localManager.GetScheme(),
		Recorder: localManager.GetEventRecorderFor("RevertCommit"),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create RevertCommit controller: %w", err))
	}

	// ChangeTransferPolicy controller must be set up first so we can
	// get the enqueue function to pass to other controllers.
	ctpReconciler := &controller.ChangeTransferPolicyReconciler{
		Client:      localManager.GetClient(),
		Scheme:      localManager.GetScheme(),
		Recorder:    localManager.GetEventRecorderFor("ChangeTransferPolicy"),
		SettingsMgr: settingsMgr,
	}
	if err = ctpReconciler.SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create ChangeTransferPolicy controller: %w", err))
	}

	if err = (&controller.CommitStatusReconciler{
		Client:      localManager.GetClient(),
		Scheme:      localManager.GetScheme(),
		Recorder:    localManager.GetEventRecorderFor("CommitStatus"),
		SettingsMgr: settingsMgr,
		EnqueueCTP:  ctpReconciler.GetEnqueueFunc(),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create CommitStatus controller: %w", err))
	}

	if err = (&controller.PromotionStrategyReconciler{
		Client:      localManager.GetClient(),
		Scheme:      localManager.GetScheme(),
		Recorder:    localManager.GetEventRecorderFor("PromotionStrategy"),
		SettingsMgr: settingsMgr,
		EnqueueCTP:  ctpReconciler.GetEnqueueFunc(),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create PromotionStrategy controller: %w", err))
	}
	if err = (&controller.ScmProviderReconciler{
		Client:   localManager.GetClient(),
		Scheme:   localManager.GetScheme(),
		Recorder: localManager.GetEventRecorderFor("ScmProvider"),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create ScmProvider controller: %w", err))
	}
	if err = (&controller.GitRepositoryReconciler{
		Client:   localManager.GetClient(),
		Scheme:   localManager.GetScheme(),
		Recorder: localManager.GetEventRecorderFor("GitRepository"),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create GitRepository controller: %w", err))
	}

	if err = (&controller.ArgoCDCommitStatusReconciler{
		Manager:            mcMgr,
		SettingsMgr:        settingsMgr,
		KubeConfigProvider: provider,
		Recorder:           localManager.GetEventRecorderFor("ArgoCDCommitStatus"),
	}).SetupWithManager(processSignalsCtx, mcMgr); err != nil {
		panic(fmt.Errorf("unable to create ArgoCDCommitStatus controller: %w", err))
	}
	if err = (&controller.ControllerConfigurationReconciler{
		Client: localManager.GetClient(),
		Scheme: localManager.GetScheme(),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create ControllerConfiguration controller: %w", err))
	}
	if err = (&controller.ClusterScmProviderReconciler{
		Client:      localManager.GetClient(),
		Scheme:      localManager.GetScheme(),
		Recorder:    localManager.GetEventRecorderFor("ClusterScmProvider"),
		SettingsMgr: settingsMgr,
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic(fmt.Errorf("unable to create ClusterScmProvider controller: %w", err))
	}
	if err := (&controller.TimedCommitStatusReconciler{
		Client:      localManager.GetClient(),
		Scheme:      localManager.GetScheme(),
		Recorder:    localManager.GetEventRecorderFor("TimedCommitStatus"),
		SettingsMgr: settingsMgr,
		EnqueueCTP:  ctpReconciler.GetEnqueueFunc(),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		panic("unable to create TimedCommitStatus controller")
	}
	if err := (&controller.GitCommitStatusReconciler{
		Client:      localManager.GetClient(),
		Scheme:      localManager.GetScheme(),
		Recorder:    localManager.GetEventRecorderFor("GitCommitStatus"),
		SettingsMgr: settingsMgr,
		EnqueueCTP:  ctpReconciler.GetEnqueueFunc(),
	}).SetupWithManager(processSignalsCtx, localManager); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitCommitStatus")
		panic(fmt.Errorf("unable to create GitCommitStatus controller: %w", err))
	}
	//+kubebuilder:scaffold:builder

	if err := localManager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		panic(fmt.Errorf("unable to set up health check: %w", err))
	}
	if err := localManager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		panic(fmt.Errorf("unable to set up ready check: %w", err))
	}

	whr := webhookreceiver.NewWebhookReceiver(localManager, webhookreceiver.EnqueueFunc(ctpReconciler.GetEnqueueFunc()))

	g, ctx := errgroup.WithContext(processSignalsCtx)

	// Initialize the provider controller with the manager
	if err := provider.SetupWithManager(ctx, mcMgr); err != nil {
		panic("unable to setup provider with manager")
	}

	g.Go(func() error {
		setupLog.Info("starting manager")
		if err := ignoreCanceled(mcMgr.Start(ctx)); err != nil {
			setupLog.Error(err, "unable to start manager")
			return err
		}
		return nil
	})

	g.Go(func() error {
		if err := ignoreCanceled(whr.Start(processSignalsCtx, fmt.Sprintf(":%d", constants.WebhookReceiverPort))); err != nil { //nolint:lll
			setupLog.Error(err, "unable to start webhook receiver")
			return err
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		setupLog.Error(err, "unable to start")
		err = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
		if err != nil {
			setupLog.Error(err, "unable to kill process")
		}
	}

	setupLog.Info("Cleaning up cloned directories")
	for _, path := range gitpaths.GetValues() {
		err := os.RemoveAll(path)
		if err != nil {
			setupLog.Error(err, "failed to cleanup directory")
		}
		setupLog.Info("cleaning directory", "directory", path)
	}
	return nil
}

func newDashboardCommand(clientConfig clientcmd.ClientConfig) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "GitOps Promoter dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			restConfig, err := clientConfig.ClientConfig()
			if err != nil {
				return fmt.Errorf("failed to get client config: %w", err)
			}

			// Add manager for the dashboard
			mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
				Scheme: scheme,
				Metrics: metricsserver.Options{
					BindAddress: ":9082",
				},
			})
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}

			// Create single signal handler
			ctx := ctrl.SetupSignalHandler()

			ws := webserver.NewWebServer(mgr)

			if err = ws.SetupWithManager(mgr); err != nil {
				panic("unable to create WebServer controller")
			}

			// Start manager in background
			go func() {
				if err := mgr.Start(ctx); err != nil {
					panic(err)
				}
			}()

			// Make port configurable
			setupLog.Info("Dashboard starting at", "port", fmt.Sprintf(" http://localhost:%d", port))

			return ws.StartDashboard(ctx, fmt.Sprintf(":%d", port))
		},
	}

	// Add default port flag
	cmd.Flags().IntVarP(&port, "port", "p", 8080, "Port to run the dashboard on")
	return cmd
}

func newAggregationServerCommand(clientConfig clientcmd.ClientConfig) *cobra.Command {
	var securePort int
	var certDir string
	var tlsCertFile string
	var tlsKeyFile string

	cmd := &cobra.Command{
		Use:   "aggregation-server",
		Short: "GitOps Promoter aggregation API server",
		Long: `The aggregation API server provides a read-only aggregated view of PromotionStrategy resources.

It exposes a virtual resource 'PromotionStrategyView' that aggregates all related resources
(GitRepository, ChangeTransferPolicy, CommitStatuses, PullRequests) for a given PromotionStrategy.

Access the aggregated view using:
  kubectl get promotionstrategyview -n <namespace> <name>

Or via the REST API:
  GET /apis/aggregation.promoter.argoproj.io/v1alpha1/namespaces/<namespace>/promotionstrategyviews/<name>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAggregationServer(clientConfig, securePort, certDir, tlsCertFile, tlsKeyFile)
		},
	}

	cmd.Flags().IntVar(&securePort, "secure-port", 6443, "The port on which to serve HTTPS with authentication and authorization.")
	cmd.Flags().StringVar(&certDir, "cert-dir", "", "The directory where the TLS certs are located. If --tls-cert-file and --tls-private-key-file are provided, this flag will be ignored.")
	cmd.Flags().StringVar(&tlsCertFile, "tls-cert-file", "", "File containing the default x509 Certificate for HTTPS.")
	cmd.Flags().StringVar(&tlsKeyFile, "tls-private-key-file", "", "File containing the default x509 private key matching --tls-cert-file.")

	return cmd
}

func runAggregationServer(clientConfig clientcmd.ClientConfig, securePort int, certDir, tlsCertFile, tlsKeyFile string) error {
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to get client config: %w", err)
	}

	// Create a controller-runtime client with watch support for querying promoter resources
	k8sClient, err := client.NewWithWatch(restConfig, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	// Set up secure serving options only
	secureServingOptions := options.NewSecureServingOptions()
	secureServingOptions.BindPort = securePort
	if certDir != "" {
		secureServingOptions.ServerCert.CertDirectory = certDir
	}
	if tlsCertFile != "" && tlsKeyFile != "" {
		secureServingOptions.ServerCert.CertKey.CertFile = tlsCertFile
		secureServingOptions.ServerCert.CertKey.KeyFile = tlsKeyFile
	}

	// Create the server config with default values
	serverConfig := genericapiserver.NewConfig(apiserver.Codecs)

	// Set the effective version (required for Complete())
	serverConfig.EffectiveVersion = compatibility.DefaultBuildEffectiveVersion()

	// Apply secure serving options
	if err := secureServingOptions.ApplyTo(&serverConfig.SecureServing); err != nil {
		return fmt.Errorf("failed to apply secure serving options: %w", err)
	}

	// Set up loopback client config (required for GenericAPIServer.New())
	// For local development, we use the same kubeconfig
	serverConfig.LoopbackClientConfig = restConfig

	// Set up minimal OpenAPI config (required even when skipping installation)
	// We provide empty definitions since we don't have generated OpenAPI specs
	namer := openapi.NewDefinitionNamer(apiserver.Scheme)
	serverConfig.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(
		func(ref common.ReferenceCallback) map[string]common.OpenAPIDefinition {
			return map[string]common.OpenAPIDefinition{}
		},
		namer,
	)
	serverConfig.OpenAPIV3Config.Info.Title = "Promoter Aggregation API"
	serverConfig.OpenAPIV3Config.Info.Version = "v1alpha1"
	// Ignore our API paths to avoid OpenAPI definition requirements
	serverConfig.OpenAPIV3Config.IgnorePrefixes = []string{"/apis/aggregation.promoter.argoproj.io"}

	// Skip OpenAPI installation since we don't have generated definitions
	serverConfig.SkipOpenAPIInstallation = true

	// Create the aggregation server config
	recommendedConfig := &genericapiserver.RecommendedConfig{
		Config: *serverConfig,
	}

	config := &apiserver.Config{
		GenericConfig: recommendedConfig,
		ExtraConfig: apiserver.ExtraConfig{
			Client: k8sClient,
		},
	}

	// Create and run the server
	server, err := config.Complete().New()
	if err != nil {
		return fmt.Errorf("failed to create aggregation server: %w", err)
	}

	// Get the address for logging
	addr := fmt.Sprintf(":%d", securePort)
	if serverConfig.SecureServing != nil && serverConfig.SecureServing.Listener != nil {
		addr = serverConfig.SecureServing.Listener.Addr().String()
	}
	setupLog.Info("Starting aggregation API server", "address", addr)

	// Create a stop channel from signal handler
	ctx := ctrl.SetupSignalHandler()
	stopCh := ctx.Done()

	// Prepare the server (sets up listeners, etc.)
	prepared := server.GenericAPIServer.PrepareRun()

	// Run the server
	return prepared.Run(stopCh)
}

func newCommand() *cobra.Command {
	var clientConfig clientcmd.ClientConfig

	opts := zap.Options{
		Development: true,
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
	}

	cmd := &cobra.Command{
		Use:   "promoter",
		Short: "GitOps Promoter",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Create the zap logger
			zapLogger := zap.New(zap.UseFlagOptions(&opts))

			// Set the controller-runtime logger
			ctrl.SetLogger(zapLogger)

			// Configure klog to use the same zap logger so all logs (including k8s client-go)
			// use the same format (JSON when --zap-encoder=json is set)
			klog.SetLogger(zapLogger)
		},
	}

	// Zap only operates on go-type flags. Cobra doesn't give us direct access to those flags.
	// So we apply the zap flags to a temp go flags set and then transfer them to the cobra flags.
	tmpZapFlagSet := flag.NewFlagSet("", flag.ContinueOnError)
	opts.BindFlags(tmpZapFlagSet)
	// Transfer flags from the temporary FlagSet to cobra's pflag.FlagSet
	tmpZapFlagSet.VisitAll(func(f *flag.Flag) {
		cmd.PersistentFlags().AddGoFlag(f)
	})

	clientConfig = addKubectlFlags(cmd.PersistentFlags())
	cmd.AddCommand(newControllerCommand(clientConfig))
	cmd.AddCommand(newDashboardCommand(clientConfig))
	cmd.AddCommand(newAggregationServerCommand(clientConfig))
	return cmd
}

func main() {
	cmd := newCommand()
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func addKubectlFlags(flags *pflag.FlagSet) clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.DefaultClientConfig = &clientcmd.DefaultClientConfig
	overrides := clientcmd.ConfigOverrides{}
	kflags := clientcmd.RecommendedConfigOverrideFlags("")
	flags.StringVar(&loadingRules.ExplicitPath, "kubeconfig", "", "Path to a kube config. Only required if out-of-cluster")
	clientcmd.BindOverrideFlags(&overrides, flags, kflags)
	return clientcmd.NewInteractiveDeferredLoadingClientConfig(loadingRules, &overrides, os.Stdin)
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

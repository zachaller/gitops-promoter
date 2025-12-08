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

package options

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/spf13/cobra"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	apiserverapi "k8s.io/apiserver/pkg/apis/apiserver"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/apiserver/pkg/util/compatibility"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	netutils "k8s.io/utils/net"

	"github.com/argoproj-labs/gitops-promoter/internal/apiserver"
	"github.com/argoproj-labs/gitops-promoter/internal/apiserver/apis/aggregated/v1alpha1"
	generatedopenapi "github.com/argoproj-labs/gitops-promoter/internal/apiserver/generated/openapi"
)

const defaultEtcdPathPrefix = "/registry/aggregated.promoter.argoproj.io"

// PromoterServerOptions contains state for the aggregated API server.
type PromoterServerOptions struct {
	RecommendedOptions *genericoptions.RecommendedOptions

	StdOut io.Writer
	StdErr io.Writer

	AlternateDNS []string
}

// NewPromoterServerOptions returns a new PromoterServerOptions.
func NewPromoterServerOptions(out, errOut io.Writer) *PromoterServerOptions {
	o := &PromoterServerOptions{
		RecommendedOptions: genericoptions.NewRecommendedOptions(
			defaultEtcdPathPrefix,
			apiserver.Codecs.LegacyCodec(v1alpha1.SchemeGroupVersion),
		),

		StdOut: out,
		StdErr: errOut,
	}

	// Disable etcd since we use in-memory storage
	o.RecommendedOptions.Etcd = nil

	return o
}

// NewCommandStartPromoterServer provides a CLI handler for 'start' command
// with a default PromoterServerOptions.
func NewCommandStartPromoterServer(ctx context.Context, defaults *PromoterServerOptions) *cobra.Command {
	o := *defaults
	cmd := &cobra.Command{
		Short: "Launch the promoter aggregated API server",
		Long:  "Launch the promoter aggregated API server",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := o.Complete(); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.RunPromoterServer(c.Context())
		},
	}
	cmd.SetContext(ctx)

	flags := cmd.Flags()
	o.RecommendedOptions.AddFlags(flags)

	return cmd
}

// Validate validates PromoterServerOptions.
func (o PromoterServerOptions) Validate() error {
	errors := []error{}
	errors = append(errors, o.RecommendedOptions.Validate()...)
	return utilerrors.NewAggregate(errors)
}

// Complete fills in fields required to have valid data.
func (o *PromoterServerOptions) Complete() error {
	return nil
}

// Config returns config for the api server given PromoterServerOptions.
func (o *PromoterServerOptions) Config() (*apiserver.Config, error) {
	// Generate self-signed certificates if not provided
	if err := o.RecommendedOptions.SecureServing.MaybeDefaultWithSelfSignedCerts(
		"localhost",
		o.AlternateDNS,
		[]net.IP{netutils.ParseIPSloppy("127.0.0.1")},
	); err != nil {
		return nil, fmt.Errorf("error creating self-signed certificates: %w", err)
	}

	serverConfig := genericapiserver.NewRecommendedConfig(apiserver.Codecs)

	// Set the feature gate and effective version (required for Complete() to work)
	serverConfig.FeatureGate = utilfeature.DefaultMutableFeatureGate
	serverConfig.EffectiveVersion = compatibility.DefaultBuildEffectiveVersion()

	// Configure OpenAPI
	serverConfig.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(
		generatedopenapi.GetOpenAPIDefinitions,
		openapi.NewDefinitionNamer(apiserver.Scheme),
	)
	serverConfig.OpenAPIConfig.Info.Title = "Promoter Aggregated API"
	serverConfig.OpenAPIConfig.Info.Version = "v1alpha1"

	serverConfig.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(
		generatedopenapi.GetOpenAPIDefinitions,
		openapi.NewDefinitionNamer(apiserver.Scheme),
	)
	serverConfig.OpenAPIV3Config.Info.Title = "Promoter Aggregated API"
	serverConfig.OpenAPIV3Config.Info.Version = "v1alpha1"

	// For local development: allow anonymous auth and use AlwaysAllow authorization
	// The kube-apiserver handles auth before proxying to us
	// In production (in-cluster), front-proxy auth will work properly
	if o.RecommendedOptions.Authentication.Anonymous == nil {
		o.RecommendedOptions.Authentication.Anonymous = &apiserverapi.AnonymousAuthConfig{}
	}
	o.RecommendedOptions.Authentication.Anonymous.Enabled = true

	if err := o.RecommendedOptions.ApplyTo(serverConfig); err != nil {
		return nil, err
	}

	// Override authorization to AlwaysAllow for local development
	// The kube-apiserver already performed authorization before proxying
	serverConfig.Authorization.Authorizer = authorizerfactory.NewAlwaysAllowAuthorizer()

	// Build the kubeconfig for connecting to the main kube-apiserver
	var kubeClientConfig *restclient.Config
	if o.RecommendedOptions.CoreAPI != nil && o.RecommendedOptions.CoreAPI.CoreAPIKubeconfigPath != "" {
		var err error
		kubeClientConfig, err = clientcmd.BuildConfigFromFlags("", o.RecommendedOptions.CoreAPI.CoreAPIKubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build kube client config from %s: %w",
				o.RecommendedOptions.CoreAPI.CoreAPIKubeconfigPath, err)
		}
	} else {
		// Fall back to in-cluster config
		var err error
		kubeClientConfig, err = restclient.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to build in-cluster kube client config: %w", err)
		}
	}

	config := &apiserver.Config{
		GenericConfig: serverConfig,
		ExtraConfig: apiserver.ExtraConfig{
			KubeClientConfig: kubeClientConfig,
		},
	}
	return config, nil
}

// RunPromoterServer starts a new PromoterAggregatedServer given PromoterServerOptions.
func (o PromoterServerOptions) RunPromoterServer(ctx context.Context) error {
	config, err := o.Config()
	if err != nil {
		return err
	}

	server, err := config.Complete().New()
	if err != nil {
		return err
	}

	return server.GenericAPIServer.PrepareRun().RunWithContext(ctx)
}

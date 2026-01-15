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

package apiserver

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aggregationv1alpha1 "github.com/argoproj-labs/gitops-promoter/internal/aggregationapi/v1alpha1"
)

var (
	// Scheme defines the runtime scheme for the aggregation API.
	Scheme = runtime.NewScheme()
	// Codecs provides encoders and decoders for the aggregation API.
	Codecs = serializer.NewCodecFactory(Scheme)
)

func init() {
	// Add standard Kubernetes types (including ListOptions, etc.) to the scheme
	utilruntime.Must(clientgoscheme.AddToScheme(Scheme))

	// Add the aggregation API types to the scheme
	utilruntime.Must(aggregationv1alpha1.AddToScheme(Scheme))
}

// ExtraConfig holds custom configuration for the aggregation API server.
type ExtraConfig struct {
	// Client is the Kubernetes client used to query promoter resources.
	Client client.Client
}

// Config defines the config for the aggregation API server.
type Config struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ExtraConfig   ExtraConfig
}

// AggregationServer contains state for a Kubernetes aggregation API server.
type AggregationServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ExtraConfig   *ExtraConfig
}

// CompletedConfig embeds a private pointer that cannot be instantiated outside of this package.
type CompletedConfig struct {
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data.
func (cfg *Config) Complete() CompletedConfig {
	c := completedConfig{
		GenericConfig: cfg.GenericConfig.Complete(),
		ExtraConfig:   &cfg.ExtraConfig,
	}

	return CompletedConfig{&c}
}

// New returns a new instance of AggregationServer from the given config.
func (c completedConfig) New() (*AggregationServer, error) {
	genericServer, err := c.GenericConfig.New("aggregation-apiserver", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}

	s := &AggregationServer{
		GenericAPIServer: genericServer,
	}

	// Install the API group
	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(aggregationv1alpha1.GroupName, Scheme, runtime.NewParameterCodec(Scheme), Codecs)

	// Get the storage for our resources
	v1alpha1storage := map[string]rest.Storage{}
	v1alpha1storage["promotionstrategyviews"] = NewPromotionStrategyViewStorage(c.ExtraConfig.Client)

	apiGroupInfo.VersionedResourcesStorageMap["v1alpha1"] = v1alpha1storage

	if err := s.GenericAPIServer.InstallAPIGroup(&apiGroupInfo); err != nil {
		return nil, err
	}

	return s, nil
}

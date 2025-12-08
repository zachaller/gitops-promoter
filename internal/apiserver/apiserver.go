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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	restclient "k8s.io/client-go/rest"

	"github.com/argoproj-labs/gitops-promoter/internal/apiserver/apis/aggregated"
	"github.com/argoproj-labs/gitops-promoter/internal/apiserver/apis/aggregated/install"
	"github.com/argoproj-labs/gitops-promoter/internal/apiserver/registry/promotionstatus"
)

var (
	// Scheme defines methods for serializing and deserializing API objects.
	Scheme = runtime.NewScheme()
	// Codecs provides methods for retrieving codecs and serializers for specific
	// versions and content types.
	Codecs = serializer.NewCodecFactory(Scheme)
	// ParameterCodec handles versioning of objects that are converted to query parameters.
	ParameterCodec = runtime.NewParameterCodec(Scheme)
)

func init() {
	install.Install(Scheme)

	// We need to add the options to empty v1
	metav1.AddToGroupVersion(Scheme, schema.GroupVersion{Version: "v1"})

	// Add unversioned types
	unversioned := schema.GroupVersion{Group: "", Version: "v1"}
	Scheme.AddUnversionedTypes(unversioned,
		&metav1.Status{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	)
}

// ExtraConfig holds custom apiserver config.
type ExtraConfig struct {
	// KubeClientConfig is the rest config used to connect to the main kube-apiserver
	// to fetch promoter CRDs (PromotionStrategy, CommitStatus, etc.)
	KubeClientConfig *restclient.Config
}

// Config defines the config for the apiserver.
type Config struct {
	ExtraConfig   ExtraConfig
	GenericConfig *genericapiserver.RecommendedConfig
}

// PromoterAggregatedServer contains state for the aggregated API server.
type PromoterAggregatedServer struct {
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

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (cfg *Config) Complete() CompletedConfig {
	c := completedConfig{
		GenericConfig: cfg.GenericConfig.Complete(),
		ExtraConfig:   &cfg.ExtraConfig,
	}

	return CompletedConfig{&c}
}

// New returns a new instance of PromoterAggregatedServer from the given config.
func (c completedConfig) New() (*PromoterAggregatedServer, error) {
	genericServer, err := c.GenericConfig.New("promoter-aggregated-apiserver", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}

	s := &PromoterAggregatedServer{
		GenericAPIServer: genericServer,
	}

	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(aggregated.GroupName, Scheme, ParameterCodec, Codecs)

	v1alpha1storage := map[string]rest.Storage{}
	// Use the kube client config to connect to the main kube-apiserver for fetching CRDs
	kubeConfig := c.ExtraConfig.KubeClientConfig
	if kubeConfig == nil {
		// Fallback to loopback config (shouldn't happen in practice)
		kubeConfig = c.GenericConfig.LoopbackClientConfig
	}
	v1alpha1storage["promotionstatuses"] = promotionstatus.NewREST(kubeConfig)
	apiGroupInfo.VersionedResourcesStorageMap["v1alpha1"] = v1alpha1storage

	if err := s.GenericAPIServer.InstallAPIGroup(&apiGroupInfo); err != nil {
		return nil, err
	}

	return s, nil
}

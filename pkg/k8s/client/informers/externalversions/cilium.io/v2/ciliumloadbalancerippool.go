// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

// Code generated by informer-gen. DO NOT EDIT.

package v2

import (
	context "context"
	time "time"

	apisciliumiov2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	versioned "github.com/cilium/cilium/pkg/k8s/client/clientset/versioned"
	internalinterfaces "github.com/cilium/cilium/pkg/k8s/client/informers/externalversions/internalinterfaces"
	ciliumiov2 "github.com/cilium/cilium/pkg/k8s/client/listers/cilium.io/v2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// CiliumLoadBalancerIPPoolInformer provides access to a shared informer and lister for
// CiliumLoadBalancerIPPools.
type CiliumLoadBalancerIPPoolInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() ciliumiov2.CiliumLoadBalancerIPPoolLister
}

type ciliumLoadBalancerIPPoolInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewCiliumLoadBalancerIPPoolInformer constructs a new informer for CiliumLoadBalancerIPPool type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewCiliumLoadBalancerIPPoolInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredCiliumLoadBalancerIPPoolInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredCiliumLoadBalancerIPPoolInformer constructs a new informer for CiliumLoadBalancerIPPool type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredCiliumLoadBalancerIPPoolInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.CiliumV2().CiliumLoadBalancerIPPools().List(context.Background(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.CiliumV2().CiliumLoadBalancerIPPools().Watch(context.Background(), options)
			},
			ListWithContextFunc: func(ctx context.Context, options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.CiliumV2().CiliumLoadBalancerIPPools().List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.CiliumV2().CiliumLoadBalancerIPPools().Watch(ctx, options)
			},
		},
		&apisciliumiov2.CiliumLoadBalancerIPPool{},
		resyncPeriod,
		indexers,
	)
}

func (f *ciliumLoadBalancerIPPoolInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredCiliumLoadBalancerIPPoolInformer(client, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *ciliumLoadBalancerIPPoolInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apisciliumiov2.CiliumLoadBalancerIPPool{}, f.defaultInformer)
}

func (f *ciliumLoadBalancerIPPoolInformer) Lister() ciliumiov2.CiliumLoadBalancerIPPoolLister {
	return ciliumiov2.NewCiliumLoadBalancerIPPoolLister(f.Informer().GetIndexer())
}

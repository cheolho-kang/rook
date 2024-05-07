/*
Copyright The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package v1

import (
	v1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/client/clientset/versioned/scheme"
	rest "k8s.io/client-go/rest"
)

type CephV1Interface interface {
	RESTClient() rest.Interface
	CephBlockPoolsGetter
	CephBlockPoolRadosNamespacesGetter
	CephBucketNotificationsGetter
	CephBucketTopicsGetter
	CephCOSIDriversGetter
	CephClientsGetter
	CephClustersGetter
	CephFilesystemsGetter
	CephFilesystemMirrorsGetter
	CephFilesystemSubVolumeGroupsGetter
	CephNFSesGetter
	CephObjectRealmsGetter
	CephObjectStoresGetter
	CephObjectStoreUsersGetter
	CephObjectZonesGetter
	CephObjectZoneGroupsGetter
	CephRBDMirrorsGetter
	NvmeOfStoragesGetter
}

// CephV1Client is used to interact with features provided by the ceph.rook.io group.
type CephV1Client struct {
	restClient rest.Interface
}

func (c *CephV1Client) CephBlockPools(namespace string) CephBlockPoolInterface {
	return newCephBlockPools(c, namespace)
}

func (c *CephV1Client) CephBlockPoolRadosNamespaces(namespace string) CephBlockPoolRadosNamespaceInterface {
	return newCephBlockPoolRadosNamespaces(c, namespace)
}

func (c *CephV1Client) CephBucketNotifications(namespace string) CephBucketNotificationInterface {
	return newCephBucketNotifications(c, namespace)
}

func (c *CephV1Client) CephBucketTopics(namespace string) CephBucketTopicInterface {
	return newCephBucketTopics(c, namespace)
}

func (c *CephV1Client) CephCOSIDrivers(namespace string) CephCOSIDriverInterface {
	return newCephCOSIDrivers(c, namespace)
}

func (c *CephV1Client) CephClients(namespace string) CephClientInterface {
	return newCephClients(c, namespace)
}

func (c *CephV1Client) CephClusters(namespace string) CephClusterInterface {
	return newCephClusters(c, namespace)
}

func (c *CephV1Client) CephFilesystems(namespace string) CephFilesystemInterface {
	return newCephFilesystems(c, namespace)
}

func (c *CephV1Client) CephFilesystemMirrors(namespace string) CephFilesystemMirrorInterface {
	return newCephFilesystemMirrors(c, namespace)
}

func (c *CephV1Client) CephFilesystemSubVolumeGroups(namespace string) CephFilesystemSubVolumeGroupInterface {
	return newCephFilesystemSubVolumeGroups(c, namespace)
}

func (c *CephV1Client) CephNFSes(namespace string) CephNFSInterface {
	return newCephNFSes(c, namespace)
}

func (c *CephV1Client) CephObjectRealms(namespace string) CephObjectRealmInterface {
	return newCephObjectRealms(c, namespace)
}

func (c *CephV1Client) CephObjectStores(namespace string) CephObjectStoreInterface {
	return newCephObjectStores(c, namespace)
}

func (c *CephV1Client) CephObjectStoreUsers(namespace string) CephObjectStoreUserInterface {
	return newCephObjectStoreUsers(c, namespace)
}

func (c *CephV1Client) CephObjectZones(namespace string) CephObjectZoneInterface {
	return newCephObjectZones(c, namespace)
}

func (c *CephV1Client) CephObjectZoneGroups(namespace string) CephObjectZoneGroupInterface {
	return newCephObjectZoneGroups(c, namespace)
}

func (c *CephV1Client) CephRBDMirrors(namespace string) CephRBDMirrorInterface {
	return newCephRBDMirrors(c, namespace)
}

func (c *CephV1Client) NvmeOfStorages(namespace string) NvmeOfStorageInterface {
	return newNvmeOfStorages(c, namespace)
}

// NewForConfig creates a new CephV1Client for the given config.
func NewForConfig(c *rest.Config) (*CephV1Client, error) {
	config := *c
	if err := setConfigDefaults(&config); err != nil {
		return nil, err
	}
	client, err := rest.RESTClientFor(&config)
	if err != nil {
		return nil, err
	}
	return &CephV1Client{client}, nil
}

// NewForConfigOrDie creates a new CephV1Client for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *CephV1Client {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}

// New creates a new CephV1Client for the given RESTClient.
func New(c rest.Interface) *CephV1Client {
	return &CephV1Client{c}
}

func setConfigDefaults(config *rest.Config) error {
	gv := v1.SchemeGroupVersion
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}

	return nil
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *CephV1Client) RESTClient() rest.Interface {
	if c == nil {
		return nil
	}
	return c.restClient
}

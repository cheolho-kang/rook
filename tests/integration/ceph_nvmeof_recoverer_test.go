/*
Copyright 2016 The Rook Authors. All rights reserved.

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

package integration

import (
	"fmt"
	"testing"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/tests/framework/clients"
	"github.com/rook/rook/tests/framework/installer"
	"github.com/rook/rook/tests/framework/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gopkg.in/yaml.v2"
)

// ************************************************
// *** Major scenarios tested by the UpgradeSuite ***
// Setup
// - Initially create a cluster from the previous minor release
// - Upgrade to the current build of Rook to verify functionality after upgrade
// - Test basic usage of block, object, and file after upgrade
// Monitors
// - One mon in the cluster
// ************************************************
func TestCephNvmeofRecovererSuite(t *testing.T) {
	s := new(NvmeofRecovererSuite)
	defer func(s *NvmeofRecovererSuite) {
		HandlePanics(recover(), s.TearDownSuite, s.T)
	}(s)
	suite.Run(t, s)
}

type NvmeofRecovererSuite struct {
	suite.Suite
	helper      *clients.TestClient
	k8sh        *utils.K8sHelper
	settings    *installer.TestCephSettings
	installer   *installer.CephInstaller
	namespace   string
	nvmeStorage cephv1.NvmeOfStorageSpec
}

func (s *NvmeofRecovererSuite) SetupSuite() {
	s.namespace = "nvmeof-recoverer"
	s.nvmeStorage = cephv1.NvmeOfStorageSpec{
		Name: "pbssd1",
		IP:   "192.168.100.14",
		Devices: []cephv1.FabricDevice{
			{
				SubNQN:       "nqn.2023-01.com.samsung.semiconductor:fc641c65-2548-4788-961f-a7ebaab3dc6a:1.3.S63UNG0T619221",
				Port:         1153,
				AttachedNode: "qemu1",
				DeviceName:   "/dev/nvme2n1",
				ClusterName:  s.namespace,
			},
			{
				SubNQN:       "nqn.2023-01.com.samsung.semiconductor:fc641c65-2548-4788-961f-a7ebaab3dc6a:1.5.S63UNG0T619219",
				Port:         1153,
				AttachedNode: "qemu2",
				DeviceName:   "/dev/nvme2n1",
				ClusterName:  s.namespace,
			},
		},
	}

	nodeDeviceMappings := make(map[string][]string)
	for _, device := range s.nvmeStorage.Devices {
		nodeDeviceMappings[device.AttachedNode] = append(nodeDeviceMappings[device.AttachedNode], device.DeviceName)
	}

	s.settings = &installer.TestCephSettings{
		ClusterName:             s.namespace,
		Namespace:               s.namespace,
		OperatorNamespace:       installer.SystemNamespace(s.namespace),
		Mons:                    1,
		EnableDiscovery:         true,
		SkipClusterCleanup:      false,
		UseHelm:                 false,
		UsePVC:                  false,
		SkipOSDCreation:         false,
		EnableVolumeReplication: false,
		NodeDeviceMappings:      nodeDeviceMappings,
		RookVersion:             installer.LocalBuildTag,
		CephVersion:             installer.ReturnCephVersion(),
	}
	s.baseSetup()
}

func (s *NvmeofRecovererSuite) TearDownSuite() {
	s.installer.UninstallRook()
}

func (s *NvmeofRecovererSuite) baseSetup() {
	s.installer, s.k8sh = StartTestCluster(s.T, s.settings)
	s.helper = clients.CreateTestClient(s.k8sh, s.installer.Manifests)
}

func (s *NvmeofRecovererSuite) TestDeployFabricDomainCluster(t *testing.T) {
	logger.Info("Start Recoverer base test")

	// Apply the nvmeofstorage CR
	nvmeStorageResource, err := yaml.Marshal(s.nvmeStorage)
	assert.Nil(t, err)
	err = s.k8sh.ResourceOperation("apply", string(nvmeStorageResource))
	assert.Nil(t, err)

	// Inject fault to the osd pod
	targetOSDID := "0"
	_, err = s.k8sh.Kubectl("-n", s.settings.Namespace, "patch", "deployment", fmt.Sprintf("rook-ceph-osd-%s", targetOSDID), "--type=json", "-p=[{\"op\": \"replace\", \"path\": \"/spec/template/spec/containers/0/command\", \"value\":[\"exit\",\"1\"]}]")
	assert.Nil(t, err)

	// Check the osd pod is removed by nvmeofstorage controller
	if !s.k8sh.WaitUntilPodWithLabelDeleted(fmt.Sprintf("ceph-osd-id=%s", targetOSDID), s.settings.Namespace) {
		assert.Fail(t, "fault OSD was not removed by nvemfostorage controller")
	}

	// Check OSD pod is transfered to another node
	expectedNextNode := "qemu2"
	assert.Nil(t, s.k8sh.WaitForPodCount(fmt.Sprintf("topology-location-host=%s", expectedNextNode), s.settings.Namespace, 1))
}

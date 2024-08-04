/*
Copyright 2024 The Rook Authors. All rights reserved.

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
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestCephNvmeofRecovererSuite(t *testing.T) {
	s := new(NvmeofRecovererSuite)
	defer func(s *NvmeofRecovererSuite) {
		HandlePanics(recover(), s.TearDownSuite, s.T)
	}(s)
	suite.Run(t, s)
}

type NvmeofRecovererSuite struct {
	suite.Suite
	helper       *clients.TestClient
	k8sh         *utils.K8sHelper
	settings     *installer.TestCephSettings
	installer    *installer.CephInstaller
	namespace    string
	nvmeStorages []cephv1.NvmeOfStorageSpec
}

func (s *NvmeofRecovererSuite) SetupSuite() {
	s.namespace = "nvmeof-recoverer"

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
		RookVersion:             installer.LocalBuildTag,
		CephVersion:             installer.ReturnCephVersion(),
	}
}

func (s *NvmeofRecovererSuite) TearDownSuite() {
	s.installer.UninstallRook()
}

func (s *NvmeofRecovererSuite) baseSetup() {
	nodeDeviceMappings := make(map[string][]string)
	for _, nvmeStorage := range s.nvmeStorages {
		for _, device := range nvmeStorage.Devices {
			nodeDeviceMappings[device.AttachedNode] = append(nodeDeviceMappings[device.AttachedNode], device.DeviceName)
		}
	}
	s.settings.NodeDeviceMappings = nodeDeviceMappings

	s.installer, s.k8sh = StartTestCluster(s.T, s.settings)
	s.helper = clients.CreateTestClient(s.k8sh, s.installer.Manifests)
}

func (s *NvmeofRecovererSuite) TestBasicSingleFabricDomain() {
	node1 := "node1"
	node2 := "node2"
	s.nvmeStorages = []cephv1.NvmeOfStorageSpec{
		{
			Name: "nvmeofstorage-pbssd1",
			IP:   "192.168.100.11",
			Devices: []cephv1.FabricDevice{
				{
					SubNQN:       "nqn.2024-07.com.example:storage1",
					Port:         1152,
					AttachedNode: node1,
					DeviceName:   "/dev/nvme0n1",
					ClusterName:  s.namespace,
				},
				{
					SubNQN:       "nqn.2024-07.com.example:storage2",
					Port:         1152,
					AttachedNode: node2,
					DeviceName:   "/dev/nvme1n1",
					ClusterName:  s.namespace,
				},
				{
					SubNQN:       "nqn.2024-07.com.example:storage3",
					Port:         1152,
					AttachedNode: node1,
					DeviceName:   "/dev/nvme2n1",
					ClusterName:  s.namespace,
				},
			},
		},
	}
	s.baseSetup()

	s.T().Run("TestDeployFabricDomainCluster", func(t *testing.T) {
		logger.Info("Start TestDeployFabricDomainCluster")
		// Apply the nvmeofstorage CR
		s.helper.RecovererClient.CreateNvmeOfStorage(s.namespace, s.nvmeStorages)

		// Check OSD failure domain
		targetDomainRecource := s.nvmeStorages[0]
		s.helper.RecovererClient.CheckOSDLocationUntilMatch(s.namespace, targetDomainRecource)
	})

	s.T().Run("TestFaultInjectionAndOSDReassign", func(t *testing.T) {
		logger.Info("Start TestFaultInjectionAndOSDReassign")

		// Get the OSD located at the target node
		targetNode := node1
		targetOSDID := s.helper.RecovererClient.GetOSDsLocatedAtNode(s.namespace, targetNode)[0]
		actualOSDLocation := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSDID)
		require.Equal(s.T(), targetNode, actualOSDLocation)

		// Inject fault to the OSD pod
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSDID)

		// Check the faulted OSD pod is removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSDID, targetNode)

		// Check OSD pod is reassigned to another node
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSDID), s.namespace, 1))
		actualOSDLocation = s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSDID)
		expectedOSDLocation := node2
		require.Equal(s.T(), expectedOSDLocation, actualOSDLocation)
	})

	s.T().Run("TestFaultInjectMultipleOSD", func(t *testing.T) {
		logger.Info("Start TestFaultInjectMultipleOSD")

		// Get the OSDs located at the target node
		targetNode1 := node2
		targetNode2 := node2
		targetOSD1ID := s.helper.RecovererClient.GetOSDsLocatedAtNode(s.namespace, targetNode1)[0]
		targetOSD2ID := s.helper.RecovererClient.GetOSDsLocatedAtNode(s.namespace, targetNode2)[1]
		actualOSD1Location := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD1ID)
		actualOSD2Location := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD2ID)
		require.Equal(s.T(), targetNode1, actualOSD1Location)
		require.Equal(s.T(), targetNode2, actualOSD2Location)

		// Inject faults to the OSD pods on the same time
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSD1ID)
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSD2ID)

		// Check the OSD pods are removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSD1ID, targetNode1)
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSD2ID, targetNode2)

		// Check OSD pods are reassgined to another node
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSD1ID), s.namespace, 1))
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSD2ID), s.namespace, 1))
		actualOSD1Location = s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD1ID)
		actualOSD2Location = s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD2ID)

		// If multiple OSDs are faulted simultaneously,
		// the OSDs that have not yet been processed can be reattached to the same node during the reassgined OSD operation.
		// Check if all of the OSDs are reassigned to the same node
		if (actualOSD1Location == targetNode1) && (actualOSD2Location == targetNode2) {
			require.Fail(s.T(), "OSDs are reassigned to the same node")
		}

		// Check if any of the OSDs are reassigned to the same node
		targetOSDID := ""
		if actualOSD1Location == targetNode1 {
			targetOSDID = targetOSD1ID
		} else if actualOSD2Location == targetNode2 {
			targetOSDID = targetOSD2ID
		}

		// Re-Inject faults to the OSD pod
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSDID)

		// Check the OSD pod is removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSDID, node2)

		// Check OSD pods are reassgined to another node
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSDID), s.namespace, 1))
		actualOSDLocation := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD1ID)
		expectedOSDLocation := node1
		require.Equal(s.T(), expectedOSDLocation, actualOSDLocation)
	})

	s.T().Run("TestFaultInjectOSDForAttachableHostsValidation", func(t *testing.T) {
		logger.Info("Start TestFaultInjectOSDForAttachableHostsValidation")

		// Get the OSD located at the target node
		targetNode := node1
		targetOSDID := s.helper.RecovererClient.GetOSDsLocatedAtNode(s.namespace, targetNode)[0]
		actualOSDLocation := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSDID)
		require.Equal(s.T(), targetNode, actualOSDLocation)

		// Inject fault to the OSD pod
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSDID)

		// Check the OSD pod is removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSDID, targetNode)

		// Check OSD pod is not transfered to another node since available attachable hosts are not enough.
		// Wait for 30 seconds to make sure the OSD pod is not reassigned to another node
		require.NotNil(s.T(), s.k8sh.WaitForLabeledPodsToRunWithRetries(fmt.Sprintf("ceph-osd-id=%s", targetOSDID), s.namespace, 6))
	})
}

func (s *NvmeofRecovererSuite) TestBasicMultiFabricDomain() {
	node1 := "smrc2-vm1"
	node2 := "smrc2-vm2"
	s.nvmeStorages = []cephv1.NvmeOfStorageSpec{
		{
			Name: "nvmeofstorage-pbssd1",
			IP:   "192.168.100.11",
			Devices: []cephv1.FabricDevice{
				{
					SubNQN:       "nqn.2024-07.com.example:storage1",
					Port:         1152,
					AttachedNode: node1,
					DeviceName:   "/dev/nvme2n1",
					ClusterName:  s.namespace,
				},
				{
					SubNQN:       "nqn.2024-07.com.example:storage2",
					Port:         1152,
					AttachedNode: node2,
					DeviceName:   "/dev/nvme2n1",
					ClusterName:  s.namespace,
				},
			},
		},
		{
			Name: "nvmeofstorage-pbssd1",
			IP:   "192.168.100.11",
			Devices: []cephv1.FabricDevice{
				{
					SubNQN:       "nqn.2024-07.com.example:storage3",
					Port:         1152,
					AttachedNode: node1,
					DeviceName:   "/dev/nvme3n1",
					ClusterName:  s.namespace,
				},
				{
					SubNQN:       "nqn.2024-07.com.example:storage4",
					Port:         1152,
					AttachedNode: node2,
					DeviceName:   "/dev/nvme4n1",
					ClusterName:  s.namespace,
				},
			},
		},
	}
	s.baseSetup()

	s.T().Run("TestDeployMultipleFabricDomainCluster", func(t *testing.T) {
		logger.Info("Start TestDeployMultipleFabricDomainCluster")
		// Apply the nvmeofstorage CR
		s.helper.RecovererClient.CreateNvmeOfStorage(s.namespace, s.nvmeStorages)

		// Check OSD failure domain
		targetDomainRecource := s.nvmeStorages[0]
		s.helper.RecovererClient.CheckOSDLocationUntilMatch(s.namespace, targetDomainRecource)

		targetDomainRecource = s.nvmeStorages[1]
		s.helper.RecovererClient.CheckOSDLocationUntilMatch(s.namespace, targetDomainRecource)
	})

	s.T().Run("TestDomain1FaultInjection", func(t *testing.T) {
		logger.Info("Start TestDomain1FaultInjection")
		// Inject fault to the osd pod
		targetNode := node1
		targetDomainRecource := s.nvmeStorages[0]
		targetOSDID := s.helper.RecovererClient.GetOSDsLocatedAtNodForDomain(s.namespace, targetNode, targetDomainRecource)[0]

		oldOSDLocation := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSDID)
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSDID)

		// Check the osd pod is removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSDID, targetNode)

		// Check OSD pod is transfered to another node
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSDID), s.namespace, 1))
		newOSDLocation := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSDID)
		require.NotEqual(s.T(), oldOSDLocation, newOSDLocation)
	})

	s.T().Run("TestFaultInjectMultipleOSDInMultiFabricDomains", func(t *testing.T) {
		logger.Info("Start TestFaultInjectMultipleOSD")
		// Inject fault to the osd pod
		targetNode := node2
		targetDomainRecource := s.nvmeStorages[0]
		targetOSD1ID := s.helper.RecovererClient.GetOSDsLocatedAtNodForDomain(s.namespace, targetNode, targetDomainRecource)[0]

		targetNode = node2
		targetDomainRecource = s.nvmeStorages[1]
		targetOSD2ID := s.helper.RecovererClient.GetOSDsLocatedAtNodForDomain(s.namespace, targetNode, targetDomainRecource)[0]

		oldOSD2Location := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD2ID)
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSD1ID)
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSD2ID)

		// Check the osd pod is removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSD1ID, targetNode)
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSD2ID, targetNode)

		// Check OSD pod is transfered to another node
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSD2ID), s.namespace, 1))
		require.NotNil(s.T(), s.k8sh.WaitForLabeledPodsToRunWithRetries(fmt.Sprintf("ceph-osd-id=%s", targetOSD1ID), s.namespace, 6))
		newOSD2Location := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD2ID)
		require.NotEqual(s.T(), oldOSD2Location, newOSD2Location)
	})

	s.T().Run("TestDomain1FaultInjectOSDForAttachableHostsValidation", func(t *testing.T) {
		logger.Info("Start TestDomain1FaultInjectOSDForAttachableHostsValidation")
		targetNode := node1
		targetDomainRecource := s.nvmeStorages[1]
		targetOSDID := s.helper.RecovererClient.GetOSDsLocatedAtNodForDomain(s.namespace, targetNode, targetDomainRecource)[0]

		// Inject fault to the osd pod
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSDID)

		// Check the osd pod is removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSDID, targetNode)

		// Check OSD pod is not transfered to another node
		require.NotNil(s.T(), s.k8sh.WaitForLabeledPodsToRunWithRetries(fmt.Sprintf("ceph-osd-id=%s", targetOSDID), s.namespace, 6))
	})
}

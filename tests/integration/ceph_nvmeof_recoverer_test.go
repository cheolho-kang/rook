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
	helper      *clients.TestClient
	k8sh        *utils.K8sHelper
	settings    *installer.TestCephSettings
	installer   *installer.CephInstaller
	namespace   string
	nvmeStorage cephv1.NvmeOfStorageSpec
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
		SkipOSDCreation:         true,
		EnableVolumeReplication: false,
		RookVersion:             installer.LocalBuildTag,
		CephVersion:             installer.ReturnCephVersion(),
	}
}

func (s *NvmeofRecovererSuite) TearDownSuite() {
	s.installer.UninstallRook()
}

func (s *NvmeofRecovererSuite) baseSetup() {
	s.installer, s.k8sh = StartTestCluster(s.T, s.settings)
	s.helper = clients.CreateTestClient(s.k8sh, s.installer.Manifests)
}

func (s *NvmeofRecovererSuite) TestBasicSingleFabricDomain() {
	node1 := "node1"
	node2 := "node2"
	s.nvmeStorage = cephv1.NvmeOfStorageSpec{
		Name:        "nvmeofstorage-pbssd1",
		IP:          "192.168.100.11",
		ClusterName: s.namespace,
		AttachableNodes: []string{
			node1,
			node2,
		},
		Devices: []cephv1.FabricDevice{
			{
				SubNQN: "nqn.2024-07.com.example:storage1",
				Port:   1152,
			},
			{
				SubNQN: "nqn.2024-07.com.example:storage2",
				Port:   1152,
			},
			{
				SubNQN: "nqn.2024-07.com.example:storage3",
				Port:   1152,
			},
		},
	}
	s.baseSetup()

	s.T().Run("TestDeployFabricDomainCluster", func(t *testing.T) {
		logger.Info("Start TestDeployFabricDomainCluster")
		// Apply the nvmeofstorage CR
		s.helper.RecovererClient.CreateNvmeOfStorage(s.namespace, s.nvmeStorage)

		// Check OSD failure domain
		targetDomainRecource := s.nvmeStorage
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

		// Find the node with the highest number of OSDs among the attachble nodes
		targetNode := s.helper.RecovererClient.GetNodeWithMostOSDs(s.namespace)

		// Get the OSDs located at the target node
		targetOSD1ID := s.helper.RecovererClient.GetOSDsLocatedAtNode(s.namespace, targetNode)[0]
		targetOSD2ID := s.helper.RecovererClient.GetOSDsLocatedAtNode(s.namespace, targetNode)[1]
		actualOSD1Location := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD1ID)
		actualOSD2Location := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD2ID)
		require.Equal(s.T(), targetNode, actualOSD1Location)
		require.Equal(s.T(), targetNode, actualOSD2Location)

		// Inject faults to the OSD pods on the same time
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSD1ID)
		s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSD2ID)

		// Check the OSD pods are removed by nvmeofstorage controller
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSD1ID, targetNode)
		s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSD2ID, targetNode)

		// Check OSD pods are reassgined to another node
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSD1ID), s.namespace, 1))
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSD2ID), s.namespace, 1))
		actualOSD1Location = s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD1ID)
		actualOSD2Location = s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD2ID)

		// If multiple OSDs are faulted simultaneously,
		// the OSDs that have not yet been processed can be reattached to the same node during the reassgined OSD operation.
		// Check if all of the OSDs are reassigned to the same node
		if (actualOSD1Location == targetNode) && (actualOSD2Location == targetNode) {
			require.Fail(s.T(), "OSDs are reassigned to the same node")
		}

		// Check if any of the OSDs are reassigned to the same node
		// Explanation: When multiple OSDs need reassignment due to faults, the operator may restart each time the CephCluster CR is updated to reflect an OSD reassignment.
		// This can lead to cases where only partially reassigned OSDs result in faulted OSDs restarting on their original nodes if the reassignment isn't fully completed.
		// To ensure clear and accurate testing, we re-inject faults to the OSD pod that is not reassigned to another node.
		targetOSDID := ""
		if actualOSD1Location == targetNode {
			targetOSDID = targetOSD1ID
		} else if actualOSD2Location == targetNode {
			targetOSDID = targetOSD2ID
		}

		// Re-Inject faults to the OSD pod that is not reassigned to another node
		if targetOSDID != "" {
			// Inject faults to the OSD pod
			s.helper.RecovererClient.InjectFaultToOSD(s.namespace, targetOSDID)

			// Check the OSD pod is removed by nvmeofstorage controller
			s.helper.RecovererClient.WaitUntilPodDeletedFromTargetNode(s.namespace, targetOSDID, node2)
		}

		// Check OSD pods are reassgined to another node
		require.Nil(s.T(), s.k8sh.WaitForPodCount(fmt.Sprintf("ceph-osd-id=%s", targetOSDID), s.namespace, 1))
		actualOSDLocation := s.helper.RecovererClient.GetNodeLocation(s.namespace, targetOSD1ID)
		expectedOSDLocation := node1
		require.Equal(s.T(), expectedOSDLocation, actualOSDLocation)
	})
}

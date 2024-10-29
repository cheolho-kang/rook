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

package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/operator/ceph/nvmeof_recoverer/nvmeofstorage"
	"github.com/rook/rook/tests/framework/installer"
	"github.com/rook/rook/tests/framework/utils"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// NvmeofRecovererOperation is a wrapper for k8s rook file operations
type NvmeofRecovererOperation struct {
	k8sh      *utils.K8sHelper
	manifests installer.CephManifests
}

// OSDNode represents the structure for the required OSD information
type OSDNode struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Children []int  `json:"children,omitempty"`
}

// CrushMap represents the CRUSH map structure
type CrushMap struct {
	Nodes []OSDNode `json:"nodes"`
}

// CreateNvmeofRecovererOperation - Constructor to create NvmeofRecovererOperation - client to perform recoverer operations on k8s
func CreateNvmeofRecovererOperation(k8shelp *utils.K8sHelper, manifests installer.CephManifests) *NvmeofRecovererOperation {
	return &NvmeofRecovererOperation{k8shelp, manifests}
}

func (n *NvmeofRecovererOperation) CreateNvmeOfStorage(namespace string, resource cephv1.NvmeOfStorageSpec) {
	nvmeofstorageResource := `
apiVersion: ceph.rook.io/v1
kind: NvmeOfStorage
metadata:
  name: ` + resource.Name + `
  namespace: ` + namespace + `
spec:
  name: ` + resource.Name + `
  ip: ` + resource.IP
	if len(resource.AttachableNodes) > 0 {
		nvmeofstorageResource += `
  attachableNodes:`
		for _, node := range resource.AttachableNodes {
			nvmeofstorageResource += `
    - "` + node + `"`
		}
	}
	if len(resource.Devices) > 0 {
		nvmeofstorageResource += `
  clusterName: "` + resource.ClusterName + `"
  devices:`
		for _, device := range resource.Devices {
			nvmeofstorageResource += `
    - subnqn: "` + device.SubNQN + `"
      port: ` + fmt.Sprintf("%d", device.Port)
		}
	}
	err := n.k8sh.ResourceOperation("apply", nvmeofstorageResource)
	require.Nil(n.k8sh.T(), err)
}

// CheckOSDLocationUntilMatch checks the OSD location until the OSDs are placed in the expected domain
func (n *NvmeofRecovererOperation) CheckOSDLocationUntilMatch(namespace string, resource cephv1.NvmeOfStorageSpec) {
	fabricHost := nvmeofstorage.FabricFailureDomainPrefix + "-" + resource.Name
	var hostOSDMap map[string][]int
	expectedNumOSDs := len(resource.Devices)
	err := wait.PollUntilContextTimeout(context.TODO(), 3*time.Second, 180*time.Second, true, func(context context.Context) (done bool, err error) {
		hostOSDMap = make(map[string][]int)
		output, err := n.k8sh.ExecToolboxWithRetry(3, namespace, "ceph", []string{"osd", "crush", "tree", "--format", "json"})
		require.Nil(n.k8sh.T(), err)

		var crushMap CrushMap
		err = json.Unmarshal([]byte(output), &crushMap)
		require.Nil(n.k8sh.T(), err)

		// Create a map to store the host and their respective OSDs
		for _, node := range crushMap.Nodes {
			if node.Type == "host" {
				host := node.Name
				for _, child := range node.Children {
					hostOSDMap[host] = append(hostOSDMap[host], child)
				}
			}
		}
		_, exists := hostOSDMap[fabricHost]
		if exists && len(hostOSDMap[fabricHost]) == expectedNumOSDs {
			return true, nil
		}

		// Retry poll until the fabric host is found
		return false, nil
	})
	require.Nil(n.k8sh.T(), err, fmt.Sprintf("Number of OSDs in domain '%s' does not match the expected count. Expected: %d, Actual: %d.", fabricHost, expectedNumOSDs, len(hostOSDMap[fabricHost])))
}

// InjectFaultToOSD injects fault to the OSD pod by patching command of the pod to exit 1
func (n *NvmeofRecovererOperation) InjectFaultToOSD(namespace, targetOSDID string) {
	ctx := context.TODO()
	err := wait.PollUntilContextTimeout(context.TODO(), 3*time.Second, 300*time.Second, true, func(context context.Context) (done bool, err error) {
		options := metav1.ListOptions{LabelSelector: fmt.Sprintf("ceph-osd-id=%s", targetOSDID)}
		pods, err := n.k8sh.Clientset.CoreV1().Pods(namespace).List(ctx, options)
		require.NoError(n.k8sh.T(), err)
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				return true, nil
			}
		}
		return false, nil
	})
	require.Nil(n.k8sh.T(), err, fmt.Sprintf("No OSD with ID '%s' found.", targetOSDID))

	_, err = n.k8sh.Kubectl("-n", namespace, "patch", "deployment", fmt.Sprintf("rook-ceph-osd-%s", targetOSDID), "--type=json",
		"-p=[{\"op\": \"replace\", \"path\": \"/spec/template/spec/containers/0/command\", \"value\":[\"exit\",\"1\"]}]",
	)

	require.Nil(n.k8sh.T(), err, "Failed inject fault to OSD.%s", targetOSDID)
}

// GetNodeLocation returns the node location of the OSD pod by querying the pod with the OSD ID
func (n *NvmeofRecovererOperation) GetNodeLocation(namespace string, targetOSDID string) string {
	options := metav1.ListOptions{LabelSelector: fmt.Sprintf("ceph-osd-id=%s", targetOSDID)}
	ctx := context.TODO()
	pods, err := n.k8sh.Clientset.CoreV1().Pods(namespace).List(ctx, options)
	require.NoError(n.k8sh.T(), err)

	return pods.Items[0].Spec.NodeName
}

// GetOSDsLocatedAtNode returns the OSD IDs located at the target node
func (n *NvmeofRecovererOperation) GetOSDsLocatedAtNode(namespace string, targetNode string) []string {
	options := metav1.ListOptions{LabelSelector: "app=rook-ceph-osd"}
	ctx := context.TODO()
	pods, err := n.k8sh.Clientset.CoreV1().Pods(namespace).List(ctx, options)
	require.NoError(n.k8sh.T(), err)

	var osdIDs []string
	for _, pod := range pods.Items {
		nodeName := pod.Spec.NodeName
		if nodeName == targetNode {
			osdIDs = append(osdIDs, pod.Labels["ceph-osd-id"])
		}
	}

	return osdIDs
}

func (n *NvmeofRecovererOperation) GetOSDsLocatedAtNodForDomain(namespace string, targetNode string, resource cephv1.NvmeOfStorageSpec) []string {
	osdIDs := n.GetOSDsLocatedAtNode(namespace, targetNode)

	targetDomain := nvmeofstorage.FabricFailureDomainPrefix + "-" + resource.Name
	output, err := n.k8sh.ExecToolboxWithRetry(3, namespace, "ceph", []string{"osd", "crush", "ls", targetDomain, "--format", "json"})
	require.Nil(n.k8sh.T(), err)

	var osds []string
	err = json.Unmarshal([]byte(output), &osds)
	require.Nil(n.k8sh.T(), err)

	var matchedIDs []string
	for _, osd := range osds {
		parts := strings.Split(osd, ".")
		if len(parts) > 1 {
			for _, existingID := range osdIDs {
				if parts[1] == existingID {
					matchedIDs = append(matchedIDs, existingID)
				}
			}
		}
	}

	require.NotEmpty(n.k8sh.T(), matchedIDs, fmt.Sprintf("No OSD with the domain '%s' found in ''%s", targetDomain, targetNode))
	return matchedIDs
}

func (n *NvmeofRecovererOperation) GetNodeWithMostOSDs(namespace string) string {
	options := metav1.ListOptions{LabelSelector: "app=rook-ceph-osd"}
	ctx := context.TODO()
	pods, err := n.k8sh.Clientset.CoreV1().Pods(namespace).List(ctx, options)
	require.NoError(n.k8sh.T(), err)

	osdMap := make(map[string]int)
	for _, pod := range pods.Items {
		nodeName := pod.Spec.NodeName
		osdMap[nodeName]++
	}

	var maxOSDs int
	var targetNode string
	for node, count := range osdMap {
		if count > maxOSDs {
			maxOSDs = count
			targetNode = node
		}
	}

	return targetNode
}

// WaitUntilPodDeletedFromTargetNode waits until the OSD pod is deleted from the target node
func (n *NvmeofRecovererOperation) WaitUntilPodDeletedFromTargetNode(namespace, targetOSDID, targetNode string) {
	options := metav1.ListOptions{LabelSelector: fmt.Sprintf("ceph-osd-id=%s", targetOSDID)}
	ctx := context.TODO()
	wait.PollUntilContextTimeout(context.TODO(), 3*time.Second, 180*time.Second, true, func(context context.Context) (done bool, err error) {
		pods, err := n.k8sh.Clientset.CoreV1().Pods(namespace).List(ctx, options)
		if kerrors.IsNotFound(err) {
			return true, nil
		}
		for _, pod := range pods.Items {
			if targetNode == pod.Spec.NodeName {
				logger.Debugf("found pods: OSD: %s, Node: %s", pod.Labels["ceph-osd-id"], pod.Spec.NodeName)
				return false, nil
			}
		}

		return true, nil
	})
}

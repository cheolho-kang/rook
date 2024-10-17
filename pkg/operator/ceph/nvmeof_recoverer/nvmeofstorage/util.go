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

package nvmeofstorage

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/rook/rook/pkg/operator/k8sutil"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Internal constants used only within this package
const (
	nvmeofToolCode = `
import json
import subprocess
import time

def get_nvme_devices():
    result = subprocess.run(['nvme', 'list', '-o', 'json'],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    devices = json.loads(result.stdout)
    return {device['DevicePath'] for device in devices.get('Devices', [])}

def connect_nvme(subnqn, ip_address, port):
    devices_before = get_nvme_devices()
    subprocess.run(['nvme', 'connect', '-t', 'tcp', '-n', subnqn, '-a', ip_address, '-s', port], check=True)
    time.sleep(1)
    devices_after = get_nvme_devices()
    new_devices = devices_after - devices_before
    if new_devices:
        print('SUCCESS:', '\\n'.join(new_devices))
    else:
        print('FAILED: No new devices connected.')

def disconnect_nvme(subnqn):
    result = subprocess.run(['nvme', 'disconnect', '-n', subnqn], stdout=subprocess.PIPE)
    output = result.stdout.decode().strip()
    if "disconnected 0 controller(s)" in output:
        print('FAILED:', output)
    else:
        print('SUCCESS:', output)

mode = "%s"
address = "%s"
port = "%s"
subnqn = "%s"

if mode == 'connect':
    connect_nvme(subnqn, address, port)
elif mode == 'disconnect':
    disconnect_nvme(subnqn)
`
)

// FabricDescriptor contains information about an OSD that is attached to a node
type FabricDescriptor struct {
	Address      string
	Port         string
	SubNQN       string
	AttachedNode string
	DeviceName   string
	ClusterName  string
}

// FabricMap manages the mapping between devices and nodes
type FabricMap struct {
	descriptorsByNode map[string][]FabricDescriptor
}

// NewFabricMap creates a new instance of FabricMap
func NewFabricMap() *FabricMap {
	return &FabricMap{
		descriptorsByNode: make(map[string][]FabricDescriptor),
	}
}

// AddDescriptor adds a fabric descriptor to the fabric map
func (o *FabricMap) AddDescriptor(fd FabricDescriptor) {
	o.descriptorsByNode[fd.AttachedNode] = append(o.descriptorsByNode[fd.AttachedNode], fd)
	logger.Debugf("added device %s to node %s", fd.SubNQN, fd.AttachedNode)
}

// RemoveDescriptor removes a fabric descriptor from the map
func (o *FabricMap) RemoveDescriptor(device FabricDescriptor) {
	fds := o.descriptorsByNode[device.AttachedNode]
	for i, fd := range fds {
		if fd.SubNQN == device.SubNQN {
			o.descriptorsByNode[device.AttachedNode] = append(fds[:i], fds[i+1:]...)
			if len(o.descriptorsByNode[device.AttachedNode]) == 0 {
				delete(o.descriptorsByNode, device.AttachedNode)
			}
			logger.Debugf("removed device %s from node %s", fd.SubNQN, fd.AttachedNode)
			break
		}
	}
}

// FindOSDBySubNQN finds the OSDInfo that has a given subnqn
func (o *FabricMap) FindDescriptorBySubNQN(subNQN string) (FabricDescriptor, error) {
	for _, devices := range o.descriptorsByNode {
		for _, device := range devices {
			if device.SubNQN == subNQN {
				return device, nil
			}
		}
	}

	return FabricDescriptor{}, fmt.Errorf("device with subnqn %s not found", subNQN)
}

// GetDescriptors returns a copy of the devicesByNode map
func (o *FabricMap) GetDescriptors() map[string][]FabricDescriptor {
	output := make(map[string][]FabricDescriptor, len(o.descriptorsByNode))
	for node, devices := range o.descriptorsByNode {
		output[node] = append([]FabricDescriptor(nil), devices...)
	}

	return output
}

// GetNextAttachableNode returns the node with the fewest fabric devices attached
func (o *FabricMap) GetNextAttachableNode(fd FabricDescriptor) string {
	// Find the faulty node that has the device attached
	faultyNode := fd.AttachedNode

	// Find nodes that can be used to reattach the device
	attachableNodes := make([]string, 0, len(o.descriptorsByNode))
	for node := range o.descriptorsByNode {
		attachableNodes = append(attachableNodes, node)
	}

	// Find the node with the least number of OSDs
	minDevices := math.MaxInt32
	var nextHost string
	for _, node := range attachableNodes {
		if node == faultyNode {
			continue
		}
		fds := o.descriptorsByNode[node]
		if len(fds) < minDevices {
			minDevices = len(fds)
			nextHost = node
		}
	}

	o.RemoveDescriptor(fd)
	return nextHost
}

// RunJob runs a Kubernetes job to execute the provided code on the target host
func RunJob(ctx context.Context, clientset kubernetes.Interface, namespace, targetHost, jobCode string) (string, error) {
	privileged := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvmeof-conn-control-job",
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nvmeof-conn-control",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nvmeof-conn-control",
							Image: "quay.io/ceph/ceph:v18",
							// TODO (cheolho.kang): Consider alternatives to the python script for attaching/detaching nvme fabric devices.
							Command: []string{"python3", "-c", jobCode},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/dev",
									Name:      "devices",
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "devices",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/dev",
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
					HostNetwork:   true,
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": targetHost,
					},
				},
			},
		},
	}

	if err := k8sutil.RunReplaceableJob(ctx, clientset, job, false); err != nil {
		logger.Errorf("failed to run job on host %s: %v", targetHost, err)
		return "", err
	}

	if err := k8sutil.WaitForJobCompletion(ctx, clientset, job, 60*time.Second); err != nil {
		result, err := k8sutil.GetPodLog(ctx, clientset, job.Namespace, fmt.Sprintf("job-name=%s", job.Name))
		if err != nil {
			logger.Errorf("failed to get logs from job on host %s: %v", targetHost, err)
			return "", err
		}
		logger.Errorf("failed to wait for job completion on host %s. err: %v, result: %s", targetHost, err, result)
		return "", err
	}

	result, err := k8sutil.GetPodLog(ctx, clientset, job.Namespace, fmt.Sprintf("job-name=%s", job.Name))
	if err != nil {
		logger.Errorf("failed to get logs from job on host %s: %v", targetHost, err)
		return "", err
	}

	logger.Debugf("successfully executed NVMe-oF job on host %s", targetHost)
	return result, nil
}

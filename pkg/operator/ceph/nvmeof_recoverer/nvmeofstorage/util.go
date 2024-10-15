package nvmeofstorage

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rook/rook/pkg/clusterd"
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

	deviceConnectCheckCode = `
import json
import subprocess

def check_nvme_connections(subnqn_list):
    result = subprocess.run(['nvme', 'list', '-o', 'json'], stdout=subprocess.PIPE)
    devices = json.loads(result.stdout).get('Devices', [])
    for subnqn in subnqn_list:
        connected_device = None
        for device in devices:
            device_path = device.get('DevicePath')
            if device_path:
                id_ctrl_result = subprocess.run(['nvme', 'id-ctrl', device_path, '-o', 'json'], stdout=subprocess.PIPE)
                id_ctrl_info = json.loads(id_ctrl_result.stdout)
                if id_ctrl_info.get('subnqn') == subnqn:
                    connected_device = device_path
                    break
        if connected_device:
            print(f'SUCCESS: {subnqn}, {connected_device}')
        else:
            print(f'FAILED: {subnqn} is not connected to any device')

subnqn_list = "%s".split(',')
check_nvme_connections(subnqn_list)
`
)

// FabricDescriptor contains information about an OSD that is attached to a node
type FabricDescriptor struct {
	Address      string
	Port         string
	SubNQN       string
	AttachedNode string
	DevicePath   string
}

// FabricMap manages the mapping between devices and nodes
type FabricMap struct {
	context          *clusterd.Context
	opManagerContext context.Context
	devicesByNode    map[string][]FabricDescriptor
	deviceBySubNQN   map[string]FabricDescriptor
}

// NewFabricMap creates a new instance of FabricMap
func NewFabricMap(context *clusterd.Context, opManagerContext context.Context) *FabricMap {
	return &FabricMap{
		context:          context,
		opManagerContext: opManagerContext,
		devicesByNode:    make(map[string][]FabricDescriptor),
		deviceBySubNQN:   make(map[string]FabricDescriptor),
	}
}

// AddDevice adds a fabric descriptor to the fabric map
func (o *FabricMap) AddDevice(fd FabricDescriptor) {
	o.deviceBySubNQN[fd.SubNQN] = fd
	o.devicesByNode[fd.AttachedNode] = append(o.devicesByNode[fd.AttachedNode], fd)
	logger.Debugf("added device %s to node %s", fd.SubNQN, fd.AttachedNode)
}

// RemoveDescriptor removes a fabric descriptor from the map
func (o *FabricMap) RemoveDescriptor(fd FabricDescriptor) {
	delete(o.deviceBySubNQN, fd.SubNQN)
	devices := o.devicesByNode[fd.AttachedNode]
	for i, device := range devices {
		if device.SubNQN == fd.SubNQN {
			o.devicesByNode[fd.AttachedNode] = append(devices[:i], devices[i+1:]...)
			break
		}
	}
	if len(o.devicesByNode[fd.AttachedNode]) == 0 {
		delete(o.devicesByNode, fd.AttachedNode)
	}
	logger.Debugf("removed device %s from node %s", fd.SubNQN, fd.AttachedNode)
}

// GetDescriptorsBySubnqn returns a copy of the deviceBySubNQN map
func (o *FabricMap) GetDescriptorsBySubnqn() map[string]FabricDescriptor {
	output := make(map[string]FabricDescriptor, len(o.deviceBySubNQN))
	for subnqn, device := range o.deviceBySubNQN {
		output[subnqn] = device
	}
	return output
}

// GetDescriptorsByNode returns a copy of the devicesByNode map
func (o *FabricMap) GetDescriptorsByNode() map[string][]FabricDescriptor {
	output := make(map[string][]FabricDescriptor, len(o.devicesByNode))
	for node, devices := range o.devicesByNode {
		output[node] = append([]FabricDescriptor(nil), devices...)
	}

	return output
}

// GetNodes returns a list of nodes in the fabric map
func (o *FabricMap) GetNodes() []string {
	nodes := make([]string, 0, len(o.devicesByNode))
	for node := range o.devicesByNode {
		nodes = append(nodes, node)
	}
	return nodes
}

// FindDescriptorsByNode returns the descriptors attached to a node
func (o *FabricMap) FindDescriptorsByNode(node string) ([]FabricDescriptor, bool) {
	devices, exists := o.devicesByNode[node]
	return devices, exists
}

// ConnectNvmeoFDevice runs a job to connect an NVMe-oF device to the target host
func ConnectNvmeoFDevice(ctx context.Context, clientset kubernetes.Interface, namespace, targetHost, address, port, subnqn string) (string, error) {
	jobCode := fmt.Sprintf(nvmeofToolCode, "connect", address, port, subnqn)
	output, err := runJob(ctx, clientset, namespace, targetHost, jobCode)
	if err != nil {
		return "", err
	}

	if !strings.Contains(output, "SUCCESS:") {
		return "", fmt.Errorf("failed to connect NVMe-oF device: %s", output)
	}

	parts := strings.SplitN(output, "SUCCESS:", 2)
	devicePath := strings.TrimSpace(parts[1])
	logger.Debugf("successfully connected NVMe-oF Device. Node: %s, DevicePath: %s, SubNQN: %s", targetHost, devicePath, subnqn)
	return devicePath, nil
}

// DisconnectNvmeoFDevice runs a job to disconnect an NVMe-oF device from the target host
func DisconnectNvmeoFDevice(ctx context.Context, clientset kubernetes.Interface, namespace, targetHost, subnqn string) (string, error) {
	jobCode := fmt.Sprintf(nvmeofToolCode, "disconnect", "", "", subnqn)
	output, err := runJob(ctx, clientset, namespace, targetHost, jobCode)
	if err != nil {
		return "", err
	}

	if !strings.Contains(output, "SUCCESS:") {
		return "", fmt.Errorf("failed to disconnect NVMe-oF device: %s", output)
	}

	logger.Debugf("successfully disconnected NVMe-oF Device. Node: %s, SubNQN: %s, Output: %s", targetHost, subnqn, output)
	return output, nil
}

// CheckNvmeConnections runs a job to check the connection status of NVMe-oF devices
func CheckNvmeConnections(ctx context.Context, clientset kubernetes.Interface, namespace, targetHost string, subnqns []string) (map[string]string, error) {
	jobCode := fmt.Sprintf(deviceConnectCheckCode, strings.Join(subnqns, ","))
	output, err := runJob(ctx, clientset, namespace, targetHost, jobCode)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`SUCCESS:\s*(.+?),\s*(.+)`)
	result := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		matches := re.FindStringSubmatch(line)
		if len(matches) == 3 {
			subnqn := strings.TrimSpace(matches[1])
			device := strings.TrimSpace(matches[2])
			result[subnqn] = device
		}
	}
	return result, nil
}

// runJob runs a Kubernetes job to execute the provided code on the target host
func runJob(ctx context.Context, clientset kubernetes.Interface, namespace, targetHost, jobCode string) (string, error) {
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

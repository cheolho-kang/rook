package clustermanager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/pkg/capnslog"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/operator/k8sutil"
	batch "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var logger = capnslog.NewPackageLogger("github.com/rook/rook", "cluster-manager")

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
    try:
        devices_before = get_nvme_devices()
        subprocess.run(['nvme', 'connect', '-t', 'tcp', '-n', subnqn,
                        '-a', ip_address, '-s', port], check=True)
        time.sleep(1)
    except subprocess.CalledProcessError as e:
        print('FAILED:', e)
    finally:
        devices_after = get_nvme_devices()
        new_devices = [device for device in devices_after if device not in devices_before]
        if new_devices:
            result = '\n'.join(new_devices)
            print(result)
        else:
            print('FAILED: No new devices connected.')

def disconnect_nvme(subnqn):
    try:
        result = subprocess.run(['nvme', 'disconnect', '-n', subnqn],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        output = result.stdout.decode('utf-8').strip()
        if "disconnected 0 controller(s)" in output:
            print('FAILED:', output)
        else:
            print(output)
    except subprocess.CalledProcessError as e:
        print('FAILED:', e)

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

type ClusterManager struct {
	context          *clusterd.Context
	opManagerContext context.Context
	fabricMap        FabricMap
}

func New(context *clusterd.Context, opManagerContext context.Context) *ClusterManager {
	return &ClusterManager{
		context:          context,
		opManagerContext: opManagerContext,
		fabricMap:        NewOSDNodeMap(),
	}
}

// AddOSD adds an OSD to the fabric map
func (cm *ClusterManager) AddOSD(osdID string, nvmeofstorage *cephv1.NvmeOfStorage) {
	for _, device := range nvmeofstorage.Spec.Devices {
		if device.OsdID == osdID {
			cm.fabricMap.AddOSD(osdID, device.AttachedNode, nvmeofstorage.Spec.IP, strconv.Itoa(device.Port), device.SubNQN)
			break
		}
	}
}

// GetNextAttachableNode returns the node with the least number of OSDs attached to it
func (cm *ClusterManager) GetNextAttachableNode(deviceInfo cephv1.FabricDevice) string {
	// Find the faulty node that has the device attached
	faultyNode := deviceInfo.AttachedNode

	// Find the node with the least number of OSDs
	attachableNodes := cm.fabricMap.GetNodes()
	minOSDs := math.MaxInt32
	var nextHost string
	for _, node := range attachableNodes {
		osds, _ := cm.fabricMap.FindOSDsByNode(node)
		if node != faultyNode && len(osds) < minOSDs {
			minOSDs = len(osds)
			nextHost = node
		}
	}

	// Remove the fault node from the map
	cm.fabricMap.RemoveOSD(nextHost, faultyNode)

	return nextHost
}

func (cm *ClusterManager) ConnectOSDDeviceToNode(namespace, targetHost string, fabricDeviceInfo cephv1.FabricDevice) (cephv1.FabricDevice, error) {
	output := *fabricDeviceInfo.DeepCopy()
	osdInfo, err := cm.fabricMap.FindOSDBySubNQN(fabricDeviceInfo.SubNQN)
	if err != nil {
		panic("OSD not found for subnqn")
	}
	newDevice, err := cm.runNvmeoFJob("connect", namespace, targetHost, osdInfo.address, osdInfo.port, osdInfo.subnqn)
	if err == nil {
		output.AttachedNode = targetHost
		output.DeviceName = newDevice
	}
	return output, err
}

func (cm *ClusterManager) DisconnectOSDDevice(namespace string, fabricDeviceInfo cephv1.FabricDevice) (string, error) {
	return cm.runNvmeoFJob("disconnect", namespace, fabricDeviceInfo.AttachedNode, "", "", fabricDeviceInfo.SubNQN)
}

func (cm *ClusterManager) runNvmeoFJob(mode, namespace, targetHost, address, port, subnqn string) (string, error) {
	privileged := true
	job := &batch.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvmeof-conn-control-job",
			Namespace: namespace,
		},
		Spec: batch.JobSpec{
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nvmeof-conn-control",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "nvmeof-conn-control",
							Image: "quay.io/ceph/ceph:v18",
							// TODO (cheolho.kang): Consider alternatives to the python script for attaching/detaching nvme fabric devices.
							Command: []string{
								"python3",
								"-c",
								fmt.Sprintf(nvmeofToolCode, string(mode), address, port, subnqn),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									MountPath: "/dev",
									Name:      "devices",
								},
							},
							SecurityContext: &v1.SecurityContext{
								Privileged: &privileged,
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "devices",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: "/dev",
								},
							},
						},
					},
					RestartPolicy: v1.RestartPolicyNever,
					HostNetwork:   true,
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": targetHost,
					},
				},
			},
		},
	}

	err := k8sutil.RunReplaceableJob(cm.opManagerContext, cm.context.Clientset, job, false)
	if err != nil {
		logger.Errorf("failed to run job. host: %s, err: %v", targetHost, err)
		return "", err
	}

	err = k8sutil.WaitForJobCompletion(cm.opManagerContext, cm.context.Clientset, job, 60*time.Second)
	if err != nil {
		logger.Errorf("failed to wait for job completion. host: %s, err: %v", targetHost, err)
		return "", err
	}

	// TODO(cheolho.kang): Need to improve the method of obtaining the success of the fabric device connect result and the path of the added device in the future.
	var output string
	output, err = k8sutil.GetPodLog(cm.opManagerContext, cm.context.Clientset, job.Namespace, fmt.Sprintf("job-name=%s", job.Name))
	if err != nil {
		logger.Errorf("failed to get logs. host: %s, err: %v", targetHost, err)
		return "", err
	}
	if strings.Contains(output, "FAILED:") {
		return "", errors.New(fmt.Sprintf("target=%s, output: %s", targetHost, output))
	}

	logger.Debugf("Successfully executed nvmeof connect/disconnect job. output: %s", output)
	return output, nil
}

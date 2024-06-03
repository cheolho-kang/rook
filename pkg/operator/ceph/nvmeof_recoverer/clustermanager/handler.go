package clustermanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/pkg/capnslog"
	"github.com/rook/rook/pkg/clusterd"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/operator/k8sutil"
	batch "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var logger = capnslog.NewPackageLogger("github.com/rook/rook", "cluster-manager")

type NvmeofMode string

// External constants used by other packages
const (
	NvmeofConnect    NvmeofMode = "connect"
	NvmeofDisconnect NvmeofMode = "disconnect"
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
	devices = {device['DevicePath'] for device in devices['Devices']}
	return devices

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
            print('SUCCESS:', result)
        else:
            print('FAILED: No new devices connected.')

def disconnect_nvme(subnqn):

	try:
		result = subprocess.run(['nvme', 'disconnect', '-n', subnqn],
								stdout=subprocess.PIPE, stderr=subprocess.PIPE)
		output = result.stdout.strip()
		print('SUCCESS:', output)
	except subprocess.CalledProcessError as e:
		print('FAILED:', e)

mode = "%s"
address = "%s"
port = "%s"
subnqn = "%s"

if mode and subnqn and address and port:
    if mode == 'connect':
        connect_nvme(subnqn, address, port)
    elif mode == 'disconnect':
        disconnect_nvme(subnqn)
`
)

type ClusterManager struct {
	context          *clusterd.Context
	opManagerContext context.Context
	OSDHostMap       map[string][]string
	AttachableHosts  []string
	HostExists       map[string]bool
}

func New(context *clusterd.Context, opManagerContext context.Context) *ClusterManager {
	return &ClusterManager{
		context:          context,
		opManagerContext: opManagerContext,
		OSDHostMap:       make(map[string][]string),
		HostExists:       make(map[string]bool),
		AttachableHosts:  []string{},
	}
}

func (cm *ClusterManager) UpdateCrushMapForOSD(namespace, clusterName, srcHostname, devicename, destHostname string) (string, error) {
	// Find the OSD ID for the given hostname and devicename
	osdID, err := cm.findOSDIDByHostAndDevice(namespace, srcHostname, devicename)
	if err != nil {
		logger.Errorf("failed to find OSD ID. targetHostname: %s, targetDeviceName: %s, err: %v", srcHostname, devicename, err)
		return "", err
	}

	// Modify the CRUSH map to relocate the OSD to the destHostname
	logger.Debugf("moving osd.%s from host %s to host %s", osdID, srcHostname, destHostname)
	cmd := []string{
		"osd",
		"crush",
		"move",
		fmt.Sprintf("osd.%s", osdID),
		"root=default",
		fmt.Sprintf("host=%s", destHostname),
	}
	buf, err := cm.executeCephCommand(namespace, clusterName, cmd)
	if err != nil {
		logger.Errorf("failed to move osd. osdID: %s, srcHost: %s, destHost: %s, err: %s", osdID, srcHostname, destHostname, string(buf))
		return "", err
	}
	cm.OSDHostMap[osdID] = append(cm.OSDHostMap[osdID], destHostname)
	if !cm.HostExists[srcHostname] {
		cm.AttachableHosts = append(cm.AttachableHosts, srcHostname)
		cm.HostExists[srcHostname] = true
	}
	return osdID, nil
}

func (cm *ClusterManager) findOSDIDByHostAndDevice(namespace, targetHostname, targetDeviceName string) (string, error) {
	// Retrieve the complete list of OSD pods
	pods, err := cm.context.Clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: "app=rook-ceph-osd",
	})
	if err != nil {
		return "", fmt.Errorf("failed to retrieve the list of pods: %s", err)
	}

	// Find the OSD ID for the given hostname and devicename
	for _, pod := range pods.Items {
		// Check if the pod is running on the target hostname
		if pod.Spec.NodeName == targetHostname {
			for _, container := range pod.Spec.Containers {
				for _, env := range container.Env {
					// Check if the pod is using the target device
					if env.Name == "ROOK_BLOCK_PATH" && env.Value == targetDeviceName {
						osdID := pod.Labels["ceph-osd-id"]
						return osdID, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no matching OSD found. targetHostname: %s, targetDevicename: %s", targetHostname, targetDeviceName)
}

func (cm *ClusterManager) executeCephCommand(namespace, clusterName string, cmd []string) ([]byte, error) {
	ctx := context.TODO()
	clusterInfo := cephclient.AdminClusterInfo(ctx, namespace, clusterName)
	exec := cephclient.NewCephCommand(cm.context, clusterInfo, cmd)
	exec.JsonOutput = true
	buf, err := exec.Run()
	if err != nil {
		// TODO (cheolho.kang): Add verification to check if the result of exec.Run matches the result of 'osd crush move'. Even if 'osd crush move' is executed.
		logger.Debugf("failed to execute ceph command. result: %s", string(buf))
		return nil, err
	}
	return buf, nil
}

func (cm *ClusterManager) GetNextAttachableHost(currentHost string) string {
	if len(cm.AttachableHosts) == 0 {
		return ""
	}
	for i, host := range cm.AttachableHosts {
		if host == currentHost {
			return cm.AttachableHosts[(i+1)%len(cm.AttachableHosts)]
		}
	}
	return ""
}

func (cm *ClusterManager) StartNvmeoFConnectJob(mode NvmeofMode, targetHost, address, port, subnqn string) (string, error) {
	privileged := true
	job := &batch.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvmeof-connect-job",
			Namespace: "rook-ceph",
		},
		Spec: batch.JobSpec{
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nvmeof-connect",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "nvmeof-connect",
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
	if strings.HasPrefix(output, "FAILED:") {
		return "", errors.New(output)
	} else if strings.HasPrefix(output, "SUCCESS:") {
		newDevice := strings.TrimSpace(strings.TrimPrefix(output, "SUCCESS:"))
		return newDevice, nil
	}

	return output, nil
}

package clustermanager

import (
	"context"
	"fmt"

	"github.com/coreos/pkg/capnslog"
	"github.com/rook/rook/pkg/clusterd"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var logger = capnslog.NewPackageLogger("github.com/rook/rook", "cluster-manager")

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
	cmd := []string{"osd", "crush", "move", fmt.Sprintf("osd.%s", osdID), fmt.Sprintf("host=%s", destHostname)}
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
			logger.Debug("next host: ", cm.AttachableHosts[(i+1)%len(cm.AttachableHosts)])
			return cm.AttachableHosts[(i+1)%len(cm.AttachableHosts)]
		}
	}
	return ""
}

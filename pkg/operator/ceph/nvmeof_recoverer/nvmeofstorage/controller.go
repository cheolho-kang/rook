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

// Package nvmeofstorage to reconcile a NvmeOfStorage CR.
package nvmeofstorage

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"emperror.dev/errors"
	"github.com/coreos/pkg/capnslog"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/operator/ceph/cluster/osd"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	"github.com/rook/rook/pkg/operator/ceph/reporting"
	"github.com/rook/rook/pkg/operator/k8sutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName            = "nvmeofstorage-controller"
	FabricFailureDomainPrefix = "fabric-host" // FabricFailureDomainPrefix is the prefix for the fabric failure domain name
)

// INITIALIZATION -> ACTIVATED
type ControllerState int

const (
	INITIALIZATION ControllerState = iota
	ACTIVATED
)

const (
	CR_UPDATED = iota
	OSD_STATE_CHANGED
)

var (
	state              = INITIALIZATION
	logger             = capnslog.NewPackageLogger("github.com/rook/rook", controllerName)
	nvmeOfStorageKind  = reflect.TypeOf(cephv1.NvmeOfStorage{}).Name()
	controllerTypeMeta = metav1.TypeMeta{
		Kind:       nvmeOfStorageKind,
		APIVersion: fmt.Sprintf("%s/%s", cephv1.CustomResourceGroup, cephv1.Version),
	}
	_ reconcile.Reconciler = &ReconcileNvmeOfStorage{}
)

// ReconcileNvmeOfStorage reconciles a NvmeOfStorage object
type ReconcileNvmeOfStorage struct {
	client           client.Client
	scheme           *runtime.Scheme
	context          *clusterd.Context
	opManagerContext context.Context
	recorder         record.EventRecorder
	fabricMap        *FabricMap
	nvmeOfStorage    *cephv1.NvmeOfStorage
}

// Add creates a new NvmeOfStorage Controller and adds it to the Manager.
func Add(mgr manager.Manager, context *clusterd.Context, opManagerContext context.Context, opConfig opcontroller.OperatorConfig) error {
	return add(mgr, newReconciler(mgr, context, opManagerContext))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, context *clusterd.Context, opManagerContext context.Context) reconcile.Reconciler {
	return &ReconcileNvmeOfStorage{
		client:           mgr.GetClient(),
		context:          context,
		scheme:           mgr.GetScheme(),
		opManagerContext: opManagerContext,
		recorder:         mgr.GetEventRecorderFor("rook-" + controllerName),
		nvmeOfStorage:    &cephv1.NvmeOfStorage{},
	}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		return fmt.Errorf("failed to create %s controller: %w", controllerName, err)
	}
	logger.Info("successfully started")

	// Watch for changes on the NvmeOfStorage CRD object
	cmKind := source.Kind(
		mgr.GetCache(),
		&cephv1.NvmeOfStorage{TypeMeta: controllerTypeMeta})
	err = c.Watch(cmKind, &handler.EnqueueRequestForObject{}, opcontroller.WatchControllerPredicate())
	if err != nil {
		return err
	}

	// Watch for changes on the OSD Pod object
	podKind := source.Kind(
		mgr.GetCache(),
		&corev1.Pod{})
	err = c.Watch(podKind, &handler.EnqueueRequestForObject{},
		predicate.Funcs{
			UpdateFunc: func(event event.UpdateEvent) bool {
				oldPod, okOld := event.ObjectOld.(*corev1.Pod)
				newPod, okNew := event.ObjectNew.(*corev1.Pod)
				if !okOld || !okNew {
					return false
				}
				if isOSDPod(newPod.Labels) && isPodDead(oldPod, newPod) {
					// Prevents redundant Reconciler triggers during the cleanup of a faulty OSD pod by the nvmeofstorage controller.
					if newPod.DeletionTimestamp != nil {
						return false
					}
					namespacedName := fmt.Sprintf("%s/%s", newPod.Namespace, newPod.Name)
					logger.Debugf("update event on Pod %q", namespacedName)
					return true
				}
				return false
			},
			CreateFunc: func(e event.CreateEvent) bool {
				return false
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		})
	if err != nil {
		return errors.Wrap(err, "failed to watch for changes on the Pod object")
	}

	return nil
}

func (r *ReconcileNvmeOfStorage) getSystemEvent(e string) ControllerState {
	if strings.Contains(e, "nvmeofstorage") {
		return CR_UPDATED
	} else if strings.Contains(e, "rook-ceph-osd") {
		return OSD_STATE_CHANGED
	}
	panic("wrong event type")
}

func (r *ReconcileNvmeOfStorage) initFabricMap(request reconcile.Request) error {
	// Fetch the NvmeOfStorage CRD object
	if err := r.client.Get(r.opManagerContext, request.NamespacedName, r.nvmeOfStorage); err != nil {
		logger.Errorf("unable to fetch NvmeOfStorage, err: %v", err)
		return err
	}

	// Initialize the fabric map
	r.fabricMap = NewFabricMap(r.nvmeOfStorage.Spec.AttachableNodes)

	// Connect the device to the target node
	for _, device := range r.nvmeOfStorage.Spec.Devices {
		fd := FabricDescriptor{
			Address: r.nvmeOfStorage.Spec.IP,
			SubNQN:  device.SubNQN,
			Port:    strconv.Itoa(device.Port),
		}
		targetNode := r.fabricMap.GetNextAttachableNode(fd)
		deviceName, err := r.connectOSDDeviceToNode(request.Namespace, targetNode, fd)
		if err != nil {
			panic(fmt.Sprintf("failed to connect device with SubNQN %s to node %s: %v",
				fd.SubNQN, fd.AttachedNode, err))
		}

		// Add the device descriptor that is connected to the target node
		fd.AttachedNode = targetNode
		fd.DeviceName = deviceName
		r.fabricMap.AddDescriptor(fd)
	}

	return r.updateCephClusterCR(request.Namespace)
}

func (r *ReconcileNvmeOfStorage) tryRelocateDevice(request reconcile.Request) error {
	// Get the osdID from the OSD pod name
	osdID := strings.Split(strings.TrimPrefix(request.Name, osd.AppName+"-"), "-")[0]

	// Get the fabric device descriptor for the given osdID
	fd, err := r.findAttachedDevice(request.Namespace, osdID)
	if err != nil {
		panic(fmt.Sprintf("%v", err))
	}

	// Cleanup the OSD that is in CrashLoopBackOff
	r.cleanupOSD(request.Namespace, osdID, fd)

	// Connect the device to the new attachable node
	r.reassignFaultedOSDDevice(request.Namespace, fd)

	// Request the OSD to be transferred to the next node
	return r.updateCephClusterCR(request.Namespace)
}

func (r *ReconcileNvmeOfStorage) Reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger.Debugf("reconciling NvmeOfStorage. Request.Namespace: %s, Request.Name: %s", request.Namespace, request.Name)

	event := r.getSystemEvent(request.Name)
	var err error
	if event == CR_UPDATED {
		if state != INITIALIZATION {
			panic("impossible")
		}
		err = r.initFabricMap(request)
		state = ACTIVATED
	} else if event == OSD_STATE_CHANGED {
		if state == INITIALIZATION {
			panic("impossible")
		}
		err = r.tryRelocateDevice(request)
		state = ACTIVATED
	} else {
		return reconcile.Result{}, nil
	}

	return reporting.ReportReconcileResult(logger, r.recorder, request, r.nvmeOfStorage, reconcile.Result{}, err)
}

// findAttachedDevice finds the attached device for the given OSD ID
func (r *ReconcileNvmeOfStorage) findAttachedDevice(namespace, osdID string) (FabricDescriptor, error) {
	opts := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("ceph-osd-id=%s", osdID),
	}
	pods, err := r.context.Clientset.CoreV1().Pods(namespace).List(r.opManagerContext, opts)
	if err != nil || len(pods.Items) != 1 {
		return FabricDescriptor{}, fmt.Errorf("failed to find OSD pod: %w", err)
	}

	// Find the device path for the given pod resource
	var deviceName string
	for _, envVar := range pods.Items[0].Spec.Containers[0].Env {
		if envVar.Name == "ROOK_BLOCK_PATH" {
			deviceName = envVar.Value
			break
		}
	}

	attachedNode := pods.Items[0].Spec.NodeName
	if fds, exists := r.fabricMap.GetDescriptors()[attachedNode]; exists {
		for _, fd := range fds {
			if fd.DeviceName == deviceName {
				return fd, nil
			}
		}
	}

	return FabricDescriptor{}, fmt.Errorf("no attached device found for OSD ID %s", osdID)
}

// cleanupOSD cleans up the OSD deployment and disconnects the device
func (r *ReconcileNvmeOfStorage) cleanupOSD(namespace, osdID string, fd FabricDescriptor) {
	// Delete the OSD deployment that is in CrashLoopBackOff
	podName := osd.AppName + "-" + osdID
	if err := k8sutil.DeleteDeployment(
		r.opManagerContext,
		r.context.Clientset,
		namespace,
		podName,
	); err != nil {
		panic(fmt.Sprintf("failed to delete OSD deployment %q in namespace %q: %v",
			podName, namespace, err))
	}
	logger.Debugf("successfully deleted the OSD deployment. Name: %q", podName)

	// Disconnect the device used by this OSD
	if _, err := r.disconnectOSDDevice(namespace, fd); err != nil {
		panic(fmt.Sprintf("failed to disconnect OSD device with SubNQN %s: %v", fd.SubNQN, err))
	}
}

func (r *ReconcileNvmeOfStorage) reassignFaultedOSDDevice(namespace string, fd FabricDescriptor) {
	targetNode := r.fabricMap.GetNextAttachableNode(fd)
	if targetNode == "" {
		// No attachable node found. OSD will be removed and rebalanced by Ceph.
		return
	}

	output, err := r.connectOSDDeviceToNode(namespace, targetNode, fd)
	if err != nil {
		// TODO (cheolho.kang): If connectOSDDeviceToNode fails due to an abnormal targetNode,
		// implement logic to exclude the current targetNode and search for the next attachable node.
		panic(fmt.Sprintf("failed to connect device with SubNQN %s to node %s: %v",
			fd.SubNQN, targetNode, err))
	}
	logger.Debugf("successfully reassigned the device. node: [%s --> %s], device: [%s --> %s], SubNQN: %s",
		fd.AttachedNode, targetNode, fd.DeviceName, output, fd.SubNQN)

	fd.AttachedNode = targetNode
	fd.DeviceName = output
	r.fabricMap.AddDescriptor(fd)
	return
}

// updateCephClusterCR updates the CephCluster CR based on the fabric map,
// performing an update only if there are differences between the existing CR and the fabric map.
func (r *ReconcileNvmeOfStorage) updateCephClusterCR(namespace string) error {
	// Fetch the CephCluster CR
	cephCluster, err := r.context.RookClientset.CephV1().CephClusters(namespace).Get(
		r.opManagerContext,
		r.nvmeOfStorage.Spec.ClusterName,
		metav1.GetOptions{},
	)
	if err != nil {
		logger.Errorf("failed to get CephCluster CR. err: %v", err)
		return err
	}

	// Check if the CephCluster CR needs to be updated
	connectedDeviceByNode := r.fabricMap.GetDescriptors()
	if !r.isCephClusterUpdateNeeded(cephCluster, connectedDeviceByNode) {
		// No changes in ceph cluster
		logger.Debug("no changes in connected devices, skipping CephCluster CR update.")
		return nil
	}

	// Update the CephCluster CR with the connected devices
	var nodes []cephv1.Node
	for nodeName, devices := range connectedDeviceByNode {
		newNode := &cephv1.Node{Name: nodeName}
		// Clone the node info from the existing CR to avoid modifying the original CR
		for _, node := range cephCluster.Spec.Storage.Nodes {
			if node.Name == nodeName {
				newNode = node.DeepCopy()
				newNode.Selection.Devices = []cephv1.Device{}
				break
			}
		}

		for _, device := range devices {
			newNode.Selection.Devices = append(newNode.Selection.Devices, cephv1.Device{
				Name: device.DeviceName,
				Config: map[string]string{
					"failureDomain": FabricFailureDomainPrefix + "-" + r.nvmeOfStorage.Spec.Name,
				},
			})
		}
		nodes = append(nodes, *newNode)
	}
	cephCluster.Spec.Storage.Nodes = nodes

	// Apply the updated CephCluster CR
	if _, err := r.context.RookClientset.CephV1().CephClusters(namespace).Update(
		r.opManagerContext,
		cephCluster,
		metav1.UpdateOptions{},
	); err != nil {
		panic(fmt.Sprintf("failed to update CephCluster CR: %v", err))
	}

	logger.Debug("CephCluster updated successfully.")

	return nil
}

// isCephClusterUpdateNeeded checks if the CephCluster CR needs to be updated
func (r *ReconcileNvmeOfStorage) isCephClusterUpdateNeeded(cephCluster *cephv1.CephCluster, connectedDeviceByNode map[string][]FabricDescriptor) bool {
	if len(cephCluster.Spec.Storage.Nodes) != len(connectedDeviceByNode) {
		return true
	}

	for _, node := range cephCluster.Spec.Storage.Nodes {
		if connectedDevices, exists := connectedDeviceByNode[node.Name]; exists {
			if len(node.Selection.Devices) != len(connectedDevices) {
				return true
			}
			deviceMap := make(map[string]struct{})
			for _, device := range node.Selection.Devices {
				deviceMap[device.Name] = struct{}{}
			}
			for _, fd := range connectedDevices {
				if _, exists := deviceMap[fd.DeviceName]; !exists {
					return true
				}
			}
		} else {
			return true
		}
	}

	return false
}

// connectOSDDeviceToNode runs a job to connect an NVMe-oF device to the target node
func (r *ReconcileNvmeOfStorage) connectOSDDeviceToNode(namespace, targetNode string, fd FabricDescriptor) (string, error) {
	output := ""
	jobCode := fmt.Sprintf(nvmeofToolCode, "connect", fd.Address, fd.Port, fd.SubNQN)
	jobOutput, err := RunJob(r.opManagerContext, r.context.Clientset, namespace, targetNode, jobCode)
	if err != nil || !strings.Contains(jobOutput, "SUCCESS:") {
		return output, fmt.Errorf("failed to connect NVMe-oF device. fd: %v, output: %s", fd, jobOutput)
	}

	parts := strings.SplitN(jobOutput, "SUCCESS:", 2)
	output = strings.TrimSpace(parts[1])

	logger.Debugf("successfully connected NVMe-oF Device. Node: %s, DevicePath: %s, SubNQN: %s", fd.AttachedNode, output, fd.SubNQN)
	return output, nil
}

// disconnectOSDDevice runs a job to disconnect an NVMe-oF device from the target node
func (r *ReconcileNvmeOfStorage) disconnectOSDDevice(namespace string, fd FabricDescriptor) (string, error) {
	jobCode := fmt.Sprintf(nvmeofToolCode, "disconnect", "", "", fd.SubNQN)
	jobOutput, err := RunJob(r.opManagerContext, r.context.Clientset, namespace, fd.AttachedNode, jobCode)
	if err != nil || !strings.Contains(jobOutput, "SUCCESS:") {
		return jobOutput, fmt.Errorf("failed to disconnect NVMe-oF device. fd: %v, output: %s", fd, jobOutput)
	}

	logger.Debugf("successfully disconnected NVMe-oF Device. Node: %s, SubNQN: %s, Output: %s", fd.AttachedNode, fd.SubNQN, jobOutput)
	return jobOutput, nil
}

func isOSDPod(labels map[string]string) bool {
	return labels["app"] == "rook-ceph-osd" && labels["ceph-osd-id"] != ""
}

func isPodDead(oldPod, newPod *corev1.Pod) bool {
	namespacedName := fmt.Sprintf("%s/%s", newPod.Namespace, newPod.Name)
	for _, cs := range newPod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			logger.Infof("OSD Pod %q is in CrashLoopBackOff, oldPod.Status.Phase: %s", namespacedName, oldPod.Status.Phase)
			return true
		}
	}

	return false
}

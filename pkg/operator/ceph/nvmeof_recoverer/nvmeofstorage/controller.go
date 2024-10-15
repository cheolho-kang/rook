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
	"math"
	"reflect"
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
)

// ReconcileNvmeOfStorage reconciles a NvmeOfStorage object
type ReconcileNvmeOfStorage struct {
	client           client.Client
	scheme           *runtime.Scheme
	context          *clusterd.Context
	opManagerContext context.Context
	recorder         record.EventRecorder
	nvmeOfStorage    *cephv1.NvmeOfStorage
	fabricMap        *FabricMap
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
		fabricMap:        NewFabricMap(context, opManagerContext),
	}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
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

	// Update existing devices to the FabricMap
	if err := r.updateExistingDevices(request.Namespace); err != nil {
		logger.Errorf("failed to update existing devices. err: %v", err)
		return err
	}

	// Filter out unconnected devices from the NvmeOfStorage CR
	existingDevices := r.fabricMap.GetDescriptorsBySubnqn()
	var unconnectedDevices []FabricDescriptor
	for _, device := range r.nvmeOfStorage.Spec.Devices {
		if _, exists := existingDevices[device.SubNQN]; !exists {
			unconnectedDevices = append(unconnectedDevices, FabricDescriptor{
				SubNQN:       device.SubNQN,
				Port:         device.Port,
				AttachedNode: device.TargetNode,
			})
		}
	}

	for _, fd := range unconnectedDevices {
		// Connect the device to the new host
		devicePath, err := ConnectNvmeoFDevice(
			r.opManagerContext,
			r.context.Clientset,
			request.Namespace,
			fd.AttachedNode,
			r.nvmeOfStorage.Spec.IP,
			fd.Port,
			fd.SubNQN,
		)
		if err != nil {
			panic(fmt.Sprintf("failed to connect device with SubNQN %s to host %s: %v",
				fd.SubNQN, fd.AttachedNode, err))
		}
		fd.DevicePath = devicePath
		r.fabricMap.AddDevice(fd)
	}

	return r.updateCephClusterCR(request.Namespace)
}

func (r *ReconcileNvmeOfStorage) tryRelocateDevice(request reconcile.Request) error {
	// Get the osdID from the OSD pod name
	osdID := strings.Split(strings.TrimPrefix(request.Name, osd.AppName+"-"), "-")[0]

	// Get the fabric device descriptor for the given request
	fd, err := r.findAttachedDevice(osdID, request)
	if err != nil {
		panic(fmt.Sprintf("%v", err))
	}

	// Cleanup the OSD that is in CrashLoopBackOff
	r.cleanupOSD(osdID, request.Namespace, fd)

	// Connect the device to the new attachable host
	if err := r.reassignFaultedOSDDevice(request.Namespace, fd); err != nil {
		logger.Errorf("failed to reassign the device. err: %v", err)
		panic(err)
	}

	// Request the OSD to be transferred to the next host
	return r.updateCephClusterCR(request.Namespace)
}

func (r *ReconcileNvmeOfStorage) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger.Debugf("reconciling NvmeOfStorage. Request.Namespace: %s, Request.Name: %s", request.Namespace, request.Name)

	event := r.getSystemEvent(request.Name)
	var err error
	if event == CR_UPDATED {
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

func (r *ReconcileNvmeOfStorage) updateExistingDevices(namespace string) error {
	nodes, err := r.context.Clientset.CoreV1().Nodes().List(r.opManagerContext, metav1.ListOptions{})
	if err != nil {
		return err
	}

	subnqns := make([]string, len(r.nvmeOfStorage.Spec.Devices))
	for i, device := range r.nvmeOfStorage.Spec.Devices {
		subnqns[i] = device.SubNQN
	}

	for _, node := range nodes.Items {
		deviceBySubNQN, err := CheckNvmeConnections(
			r.opManagerContext,
			r.context.Clientset,
			namespace,
			node.Name,
			subnqns,
		)
		if err != nil {
			return err
		}

		for subnqn, devicePath := range deviceBySubNQN {
			r.fabricMap.AddDevice(FabricDescriptor{
				AttachedNode: node.Name,
				SubNQN:       subnqn,
				DevicePath:   devicePath,
			})
		}
	}

	logger.Info("Successfully updated existing devices.")
	return nil
}

// isCephClusterUpdateNeeded checks if the CephCluster CR needs to be updated
func (r *ReconcileNvmeOfStorage) isCephClusterUpdateNeeded(cephCluster *cephv1.CephCluster, connectedDeviceByNode map[string][]FabricDescriptor) bool {
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
				if _, exists := deviceMap[fd.DevicePath]; !exists {
					return true
				}
			}
		} else {
			return true
		}
	}

	return false
}

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

	connectedDeviceByNode := r.fabricMap.GetDescriptorsByNode()

	if !r.isCephClusterUpdateNeeded(cephCluster, connectedDeviceByNode) {
		logger.Debug("No changes in connected devices, skipping CephCluster CR update.")
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
				Name: device.DevicePath,
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
		return fmt.Errorf("failed to update CephCluster CR: %w", err)
	}

	logger.Debug("CephCluster updated successfully.")

	return nil
}

func (r *ReconcileNvmeOfStorage) findAttachedDevice(osdID string, request reconcile.Request) (FabricDescriptor, error) {
	opts := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("ceph-osd-id=%s", osdID),
	}
	pods, err := r.context.Clientset.CoreV1().Pods(request.Namespace).List(r.opManagerContext, opts)
	if err != nil || len(pods.Items) != 1 {
		return FabricDescriptor{}, fmt.Errorf("failed to find OSD pod: %w", err)
	}

	// Find the device path for the given pod resource
	attachedNode := pods.Items[0].Spec.NodeName
	var devicePath string
	for _, envVar := range pods.Items[0].Spec.Containers[0].Env {
		if envVar.Name == "ROOK_BLOCK_PATH" {
			devicePath = envVar.Value
			break
		}
	}

	if fds, exists := r.fabricMap.FindDescriptorsByNode(attachedNode); exists {
		for _, fd := range fds {
			if fd.DevicePath == devicePath {
				return fd, nil
			}
		}
	}

	return FabricDescriptor{}, fmt.Errorf("no attached device found for OSD ID %s", osdID)
}

// cleanupOSD cleans up the OSD deployment and disconnects the device
func (r *ReconcileNvmeOfStorage) cleanupOSD(osdID, namespace string, fd FabricDescriptor) {
	// Delete the OSD deployment that is in CrashLoopBackOff
	podName := osd.AppName + "-" + osdID
	if err := k8sutil.DeleteDeployment(r.opManagerContext, r.context.Clientset, namespace, podName); err != nil {
		panic(fmt.Sprintf("failed to delete OSD deployment %q: %v", podName, err))
	}
	logger.Debugf("successfully deleted the OSD deployment: %q", podName)

	if _, err := DisconnectNvmeoFDevice(
		r.opManagerContext,
		r.context.Clientset,
		namespace,
		fd.AttachedNode,
		fd.SubNQN,
	); err != nil {
		panic(fmt.Sprintf("failed to disconnect OSD device with SubNQN %s: %v", fd.SubNQN, err))
	}
}

func (r *ReconcileNvmeOfStorage) reassignFaultedOSDDevice(namespace string, fd FabricDescriptor) error {
	nextHostName := r.getNextAttachableHost(fd)
	if nextHostName == "" {
		// No attachable host available. OSD will be removed and rebalanced by Ceph
		return nil
	}

	devicePath, err := ConnectNvmeoFDevice(
		r.opManagerContext,
		r.context.Clientset,
		namespace,
		nextHostName,
		r.nvmeOfStorage.Spec.IP,
		fd.Port,
		fd.SubNQN,
	)
	if err != nil {
		return fmt.Errorf("failed to connect device with SubNQN %s to host %s: %w", fd.SubNQN, nextHostName, err)
	}

	newDescriptor := FabricDescriptor{
		SubNQN:       fd.SubNQN,
		Port:         fd.Port,
		AttachedNode: nextHostName,
		DevicePath:   devicePath,
	}
	r.fabricMap.AddDevice(newDescriptor)
	logger.Debugf("successfully reassigned the device. host: [%s --> %s], device: [%s --> %s], SubNQN: %s",
		fd.AttachedNode, newDescriptor.AttachedNode, fd.DevicePath, newDescriptor.DevicePath, newDescriptor.SubNQN)

	return nil
}

// getNextAttachableHost returns the node with the least number of OSDs attached to it
func (r *ReconcileNvmeOfStorage) getNextAttachableHost(fd FabricDescriptor) string {
	faultyNode := fd.AttachedNode

	attachableNodes := r.fabricMap.GetNodes()
	minDevices := math.MaxInt32
	var nextHost string
	// Find the node with the least number of OSDs
	for _, node := range attachableNodes {
		if node == faultyNode {
			continue
		}
		fds, _ := r.fabricMap.FindDescriptorsByNode(node)
		if len(fds) < minDevices {
			minDevices = len(fds)
			nextHost = node
		}
	}

	r.fabricMap.RemoveDescriptor(fd)
	return nextHost
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

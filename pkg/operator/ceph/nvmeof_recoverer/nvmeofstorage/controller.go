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
	"strings"

	"emperror.dev/errors"
	"github.com/coreos/pkg/capnslog"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/operator/ceph/cluster/osd"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	cm "github.com/rook/rook/pkg/operator/ceph/nvmeof_recoverer/clustermanager"
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
	clustermanager   *cm.ClusterManager
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
		clustermanager:   cm.New(context, opManagerContext),
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

func (r *ReconcileNvmeOfStorage) initFabricMap(context context.Context, request reconcile.Request) error {
	// Fetch the NvmeOfStorage CRD object
	if err := r.client.Get(r.opManagerContext, request.NamespacedName, r.nvmeOfStorage); err != nil {
		logger.Errorf("unable to fetch NvmeOfStorage, err: %v", err)
		return err
	}

	r.reconstructCRUSHMap(context, request.Namespace)

	// Update the NvmeOfStorage CR to reflect the OSD ID
	if err := r.client.Update(context, r.nvmeOfStorage); err != nil {
		panic(fmt.Sprintf("Failed to update NVMeOfStorage: %v, Namespace: %s, Name: %s", err, request.Namespace, request.Name))
	}
	return nil
}

func (r *ReconcileNvmeOfStorage) tryRelocateDevice(request reconcile.Request) error {
	// Get the osdID from the OSD pod name
	osdID := strings.Split(strings.TrimPrefix(request.Name, osd.AppName+"-"), "-")[0]
	deviceInfo := r.findTargetNvmeOfStorageCR(osdID)

	// Cleanup the OSD that is in CrashLoopBackOff
	r.cleanupOSD(request.Namespace, deviceInfo)

	// Connect the device to the new attachable host
	newDeviceInfo := r.reassignFaultedOSDDevice(request.Namespace, deviceInfo)

	// Request the OSD to be transferred to the next host
	return r.updateCephClusterCR(request.Namespace, deviceInfo, newDeviceInfo)
}

func (r *ReconcileNvmeOfStorage) Reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger.Debugf("reconciling NvmeOfStorage. Request.Namespace: %s, Request.Name: %s", request.Namespace, request.Name)

	event := r.getSystemEvent(request.Name)
	var err error
	if event == CR_UPDATED {
		if state != INITIALIZATION {
			panic("impossible")
		}
		err = r.initFabricMap(context, request)
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

func (r *ReconcileNvmeOfStorage) reconstructCRUSHMap(context context.Context, namespace string) {
	// Retrieve the nvmeofstorage CR to update the CRUSH map
	for i := range r.nvmeOfStorage.Spec.Devices {
		device := &r.nvmeOfStorage.Spec.Devices[i]
		// Get OSD pods with label "app=rook-ceph-osd"
		var clusterName string
		opts := metav1.ListOptions{
			LabelSelector: "app=" + osd.AppName,
		}
		pods := r.getPods(context, namespace, opts)
		// Find the osd id and cluster name for the fabric device listed in the nvmeofstorage CR
		for _, pod := range pods.Items {
			for _, envVar := range pod.Spec.Containers[0].Env {
				if pod.Spec.NodeName == device.AttachedNode && envVar.Name == "ROOK_BLOCK_PATH" && envVar.Value == device.DeviceName {
					device.OsdID = pod.Labels["ceph-osd-id"]
					clusterName = pod.Labels["app.kubernetes.io/part-of"]
					crushRoot := pod.Labels["topology-location-root"]

					// Update CRUSH map for OSD relocation to fabric failure domain
					fabricHost := FabricFailureDomainPrefix + "-" + r.nvmeOfStorage.Spec.Name
					clusterInfo := cephclient.AdminClusterInfo(context, namespace, clusterName)
					cmd := []string{"osd", "crush", "move", fmt.Sprintf("osd.%s", device.OsdID), fmt.Sprintf("root=%s", crushRoot), fmt.Sprintf("host=%s", fabricHost)}
					exec := cephclient.NewCephCommand(r.context, clusterInfo, cmd)
					exec.JsonOutput = true
					buf, err := exec.Run()
					if err != nil {
						logger.Error(err, "Failed to move osd", "osdID", device.OsdID, "srcHost", device.AttachedNode,
							"destHost", fabricHost, "result", string(buf))
						panic(err)
					}
					logger.Debugf("Successfully updated CRUSH Map. osdID: %s, srcHost: %s, destHost: %s",
						device.OsdID, device.AttachedNode, fabricHost)

					// Update the OSD deployment depending on the nvmeofstorage CR
					r.clustermanager.AddOSD(device.OsdID, r.nvmeOfStorage)
				}
			}
		}
	}
}

func (r *ReconcileNvmeOfStorage) findTargetNvmeOfStorageCR(osdID string) cephv1.FabricDevice {
	for _, device := range r.nvmeOfStorage.Spec.Devices {
		if device.OsdID == osdID {
			return device
		}
	}
	panic("no attached node found")
}

func (r *ReconcileNvmeOfStorage) getPods(context context.Context, namespace string, opts metav1.ListOptions) *corev1.PodList {
	pods, err := r.context.Clientset.CoreV1().Pods(namespace).List(context, opts)
	if err != nil || len(pods.Items) == 0 {
		panic(err)
	}
	return pods
}

// cleanupOSD cleans up the OSD deployment and disconnects the device
func (r *ReconcileNvmeOfStorage) cleanupOSD(namespace string, deviceInfo cephv1.FabricDevice) {
	// Delete the OSD deployment that is in CrashLoopBackOff
	podName := osd.AppName + "-" + deviceInfo.OsdID
	if err := k8sutil.DeleteDeployment(
		r.opManagerContext,
		r.context.Clientset,
		namespace,
		podName,
	); err != nil {
		panic(fmt.Sprintf("failed to delete OSD deployment %q in namespace %q: %v",
			podName, namespace, err))
	}

	// Disconnect the device used by this OSD
	if _, err := r.clustermanager.DisconnectOSDDevice(namespace, deviceInfo); err != nil {
		panic(fmt.Sprintf("failed to disconnect OSD device with SubNQN %s: %v", deviceInfo.SubNQN, err))
	}
	logger.Debugf("successfully deleted the OSD deployment. Name: %q", podName)
}

func (r *ReconcileNvmeOfStorage) reassignFaultedOSDDevice(namespace string, deviceInfo cephv1.FabricDevice) cephv1.FabricDevice {
	nextHostName := r.clustermanager.GetNextAttachableHost(deviceInfo.OsdID)

	if nextHostName == "" {
		// Return an empty struct when there is no attachable host, which means this OSD will be removed and rebalanced by Ceph
		return cephv1.FabricDevice{}
	}

	output, err := r.clustermanager.ConnectOSDDeviceToHost(namespace, nextHostName, deviceInfo)
	if err != nil {
		panic(fmt.Sprintf("failed to connect device with SubNQN %s to host %s: %v",
			deviceInfo.SubNQN, nextHostName, err))
	}

	// Update the attached node for reassigning the device
	r.clustermanager.AddOSD(output.OsdID, r.nvmeOfStorage)

	// TODO (cheolho.kang): these lines should be moved to initialization phase. Other updatable data (e.g., device name, attached node) should be separated from CR and managed via k8s (e.g., etcd, configmap) (PBDEV-1748)
	// Update the NvmeOfStorage CR
	for i := range r.nvmeOfStorage.Spec.Devices {
		device := &r.nvmeOfStorage.Spec.Devices[i]
		if device.OsdID == deviceInfo.OsdID {
			if output.AttachedNode == "" {
				// it means no nodes are available for reassignment
				// In this case, the device will be removed from the nvmeOfStorage CR
				r.nvmeOfStorage.Spec.Devices = append(r.nvmeOfStorage.Spec.Devices[:i], r.nvmeOfStorage.Spec.Devices[i+1:]...)
				logger.Debug("OSD.%s will not be reassigned to any node", device.OsdID)
			} else {
				device.AttachedNode = output.AttachedNode
				device.DeviceName = output.DeviceName
			}
			break
		}
	}
	err = r.client.Update(r.opManagerContext, r.nvmeOfStorage)
	if err != nil {
		panic(fmt.Sprintf("failed to update NVMeOfStorage: %s, error: %+v", r.nvmeOfStorage.Name, err))
	}

	logger.Debugf("successfully reassigned the device for OSD.%s. host: [%s --> %s], device: [%s --> %s], SubNQN: %s",
		output.OsdID, deviceInfo.AttachedNode, output.AttachedNode, deviceInfo.DeviceName, output.DeviceName, output.SubNQN)

	return output
}

func (r *ReconcileNvmeOfStorage) updateCephClusterCR(namespace string, oldDeviceInfo, newDeviceInfo cephv1.FabricDevice) error {
	// Fetch the CephCluster CR
	cephCluster, err := r.context.RookClientset.CephV1().CephClusters(namespace).Get(
		r.opManagerContext,
		newDeviceInfo.ClusterName,
		metav1.GetOptions{},
	)
	if err != nil {
		logger.Errorf("failed to get CephCluster CR. err: %v", err)
		return err
	}

	// Update the devices for the CephCluster CR
	for i, node := range cephCluster.Spec.Storage.Nodes {
		if node.Name == oldDeviceInfo.AttachedNode {
			// Remove the device from the old node
			var filteredDevices []cephv1.Device
			for _, device := range node.Devices {
				if device.Name != oldDeviceInfo.DeviceName {
					filteredDevices = append(filteredDevices, device)
				}
			}
			cephCluster.Spec.Storage.Nodes[i].Devices = filteredDevices
		} else if node.Name == newDeviceInfo.AttachedNode {
			// Add the new device to the new node
			fabricHost := FabricFailureDomainPrefix + "-" + r.nvmeOfStorage.Spec.Name
			newDevice := cephv1.Device{
				Name: newDeviceInfo.DeviceName,
				Config: map[string]string{
					"failureDomain": fabricHost,
				},
			}
			// Check for existing device with the same name
			for _, device := range node.Devices {
				if device.Name == newDeviceInfo.DeviceName {
					panic(fmt.Sprintf("device %s already exists in the new host", newDeviceInfo.DeviceName))
				}
			}
			cephCluster.Spec.Storage.Nodes[i].Devices = append(node.Devices, newDevice)
			break
		}
	}

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

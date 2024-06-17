/*
Copyright 2016 The Rook Authors. All rights reserved.

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
	"encoding/json"
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
	cm "github.com/rook/rook/pkg/operator/ceph/nvmeof_recoverer/clustermanager"
	"github.com/rook/rook/pkg/operator/ceph/reporting"
	"github.com/rook/rook/pkg/operator/k8sutil"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
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
	controllerName = "nvmeofstorage-controller"
)

var logger = capnslog.NewPackageLogger("github.com/rook/rook", controllerName)

var nvmeOfStorageKind = reflect.TypeOf(cephv1.NvmeOfStorage{}).Name()

// Sets the type meta for the controller main object
var controllerTypeMeta = metav1.TypeMeta{
	Kind:       nvmeOfStorageKind,
	APIVersion: fmt.Sprintf("%s/%s", cephv1.CustomResourceGroup, cephv1.Version),
}

var _ reconcile.Reconciler = &ReconcileNvmeOfStorage{}

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

// Add creates a new NvmeOfStorage Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
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
		clustermanager:   cm.New(context, opManagerContext),
		nvmeOfStorage:    &cephv1.NvmeOfStorage{},
	}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		return errors.Wrapf(err, "failed to create %s controller", controllerName)
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

func (r *ReconcileNvmeOfStorage) Reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	reconcileResponse, err := r.reconcile(context, request)
	return reconcileResponse, err
}

func (r *ReconcileNvmeOfStorage) reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger.Debugf("reconciling NvmeOfStorage. Request.Namespace: %s, Request.Name: %s", request.Namespace, request.Name)

	if strings.Contains(request.Name, "nvmeofstorage") {
		// Fetch the NvmeOfStorage CRD object
		err := r.fetchNvmeOfStorage(r.nvmeOfStorage, request)
		if err != nil {
			return reconcile.Result{}, err
		}

		// Update the crush map with the devices in the NvmeOfStorage CR
		for i := range r.nvmeOfStorage.Spec.Devices {
			device := &r.nvmeOfStorage.Spec.Devices[i]
			osdID, err := r.clustermanager.UpdateCrushMapForOSD("rook-ceph", "my-cluster", device.AttachedNode, device.DeviceName, "fabric-host-"+r.nvmeOfStorage.Spec.Name)
			if err != nil {
				logger.Debugf("failed to update CRUSH Map. targetNode: %s, targetDevice: %s, err: %s", device.AttachedNode, device.DeviceName, err)
				continue
			}
			device.OsdID = osdID
			logger.Debugf("successfully updated CRUSH Map. targetNode: %s, targetDevice: %s", device.AttachedNode, device.DeviceName)
		}
		err = r.updateCR(context, request, r.nvmeOfStorage)
		if err != nil {
			return reconcile.Result{}, err
		}

		return reporting.ReportReconcileResult(logger, r.recorder, request, r.nvmeOfStorage, reconcile.Result{}, err)
	} else if strings.Contains(request.Name, "rook-ceph-osd") {
		osdId, err := extractOSDID(request.Name)
		if err != nil {
			return reconcile.Result{}, err
		}

		var nextHostName string
		var targetOSDInfo cephv1.FabricDevice
		for _, device := range r.nvmeOfStorage.Spec.Devices {
			if device.OsdID == osdId {
				nextHostName = r.clustermanager.GetNextAttachableHost(device.AttachedNode)
				if nextHostName == "" {
					logger.Debugf("no attachable hosts found")
					return reconcile.Result{}, nil
				}
				targetOSDInfo = device
				break
			}
		}

		// Delete the OSD deployment that is in CrashLoopBackOff
		err = k8sutil.DeleteDeployment(
			r.opManagerContext,
			r.context.Clientset,
			request.Namespace,
			"rook-ceph-osd-"+osdId,
		)
		if err != nil {
			panic(err)
		}
		logger.Debugf("successfully deleted the OSD.%s deployment", osdId)

		// Disconnect the fabric device used by the OSD
		_, err = r.clustermanager.StartNvmeoFConnectJob(cm.NvmeofDisconnect, nextHostName,
			r.nvmeOfStorage.Spec.IP, strconv.Itoa(targetOSDInfo.Port), targetOSDInfo.SubNQN)
		if err != nil {
			return reconcile.Result{}, err
		}

		// Connect the device to the next host for fast recovery
		var newDevice string
		newDevice, err = r.clustermanager.StartNvmeoFConnectJob(cm.NvmeofConnect, nextHostName,
			r.nvmeOfStorage.Spec.IP, strconv.Itoa(targetOSDInfo.Port), targetOSDInfo.SubNQN)
		if err != nil {
			return reconcile.Result{}, err
		}
		logger.Debugf("successfully connected device to new host. targetHost: %s newDevice: %s", nextHostName, newDevice)

		// Request the OSD to be transferred to the next host
		r.createOSDTransferConfig(osdId, r.nvmeOfStorage.Name, nextHostName, request.Namespace)
		err = r.updateCephClusterDevices(
			request.Namespace, r.nvmeOfStorage.Spec.ClusterName,
			targetOSDInfo.DeviceName, targetOSDInfo.AttachedNode, newDevice, nextHostName)
		if err != nil {
			return reconcile.Result{}, err
		}

	return reconcile.Result{}, nil
}

func (r *ReconcileNvmeOfStorage) createOSDTransferConfig(osdId, faultDomain, nextHostName, namespace string) {
	var osdTransferInfo osd.OSDTransferInfo
	osdTransferInfo.ID, _ = strconv.Atoi(osdId)
	osdTransferInfo.Node = nextHostName
	osdTransferInfo.FaultDomain = faultDomain
	configInBytes, err := json.Marshal(osdTransferInfo)
	if err != nil {
		logger.Errorf("failed to marshal osd replace config. osdID: %s, nextHostName: %s, faultDomain: %s", osdId, faultDomain, nextHostName)
		return
	}

	newConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      osd.OSDTransferConfigName,
			Namespace: namespace,
		},
		Data: map[string]string{
			osd.OSDTransferConfigKey: string(configInBytes),
		},
	}

	_, err = k8sutil.CreateOrUpdateConfigMap(r.opManagerContext, r.context.Clientset, newConfigMap)
	if err != nil {
		logger.Errorf("failed to create or update %q configMap", newConfigMap.Name)
		return
	}
	logger.Debug("successfully created or updated configMap, name: ", newConfigMap.Name, ", osdTransferInfo: ", osdTransferInfo)
}

func (r *ReconcileNvmeOfStorage) updateCR(context context.Context, request reconcile.Request, nvmeOfStorage *cephv1.NvmeOfStorage) error {
	err := r.client.Update(context, nvmeOfStorage)
	if err != nil {
		logger.Error(err, "Failed to update NVMeOfStorage", "Namespace", request.Namespace, "Name", request.Name)
		return err
	}
	return nil
}

// fetchNvmeOfOSD retrieves the NvmeOfOSD instance by name and namespace.
func (r *ReconcileNvmeOfStorage) fetchNvmeOfStorage(nvmeOfStorage *cephv1.NvmeOfStorage, request reconcile.Request) error {
	err := r.client.Get(r.opManagerContext, request.NamespacedName, nvmeOfStorage)
	if err != nil {
		logger.Errorf("unable to fetch NvmeOfStorage, err: %v", err)
		return err
	}
	return nil
}

func (r *ReconcileNvmeOfStorage) updateCephClusterDevices(namespace, clusterName, oldDevicePath, oldNode, newDevicePath, newNode string) error {
	cephCluster, err := r.context.RookClientset.CephV1().CephClusters(namespace).Get(context.Background(), clusterName, metav1.GetOptions{})
	if err != nil {
		logger.Errorf("failed to get cluster CR. err: %v", err)
		return err
	}

	// Update the devices in the CephCluster CR
	for i, node := range cephCluster.Spec.Storage.Nodes {
		if node.Name == oldNode {
			var updatedDevices []cephv1.Device
			for _, device := range node.Devices {
				if device.Name != oldDevicePath {
					updatedDevices = append(updatedDevices, device)
				}
			}
			cephCluster.Spec.Storage.Nodes[i].Devices = updatedDevices
		}
	}

	for i, node := range cephCluster.Spec.Storage.Nodes {
		if node.Name == newNode {
			newDevice := cephv1.Device{Name: newDevicePath}
			for _, device := range node.Devices {
				if device.Name == newDevicePath {
					panic(fmt.Sprintf("device %s already exists in the new host", newDevicePath))
				}
			}
			cephCluster.Spec.Storage.Nodes[i].Devices = append(node.Devices, newDevice)
			break
		}
	}

	_, err = r.context.RookClientset.CephV1().CephClusters(namespace).Update(context.TODO(), cephCluster, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	logger.Debug("CephCluster updated successfully. oldDevicePath: ", oldDevicePath, ", oldNode: ", oldNode, ", newDevicePath: ", newDevicePath, ", newNode: ", newNode)

	return nil
}

func isOSDPod(labels map[string]string) bool {
	if labels["app"] == "rook-ceph-osd" && labels["ceph-osd-id"] != "" {
		logger.Debugf("OSD Pod found. ceph-osd-id: %s", labels["ceph-osd-id"])
		return true
	}

	return false
}

func isPodDead(oldPod *corev1.Pod, newPod *corev1.Pod) bool {
	namespacedName := fmt.Sprintf("%s/%s", newPod.Namespace, newPod.Name)
	for _, cs := range newPod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			logger.Infof("OSD Pod %q is in CrashLoopBackOff, oldPod.Status.Phase: %s", namespacedName, oldPod.Status.Phase)
			return true
		}
	}

	return false
}

func extractOSDID(podName string) (string, error) {
	parts := strings.Split(podName, "-")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid pod name format")
	}

	osdID := parts[3]
	return osdID, nil
}

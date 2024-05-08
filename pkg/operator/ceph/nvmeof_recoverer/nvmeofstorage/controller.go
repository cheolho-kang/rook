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
	"fmt"
	"reflect"
	"strconv"

	"emperror.dev/errors"
	"github.com/coreos/pkg/capnslog"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	"github.com/rook/rook/pkg/operator/ceph/reporting"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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

	return nil
}

func (r *ReconcileNvmeOfStorage) Reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	reconcileResponse, err := r.reconcile(request)
	return reconcileResponse, err
}

func (r *ReconcileNvmeOfStorage) reconcile(request reconcile.Request) (reconcile.Result, error) {
	logger.Debug("reconciling NvmeOfStorage", "Request.Namespace", request.Namespace, "Request.Name", request.Name)

	// Fetch the NvmeOfStorage CRD object
	nvmeOfOSD, err := r.fetchNvmeOfStorage(request)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = r.createNvmeOfOSD(nvmeOfOSD)
	if err != nil {
		return reconcile.Result{}, err
	}

	return reporting.ReportReconcileResult(logger, r.recorder, request, nvmeOfOSD, reconcile.Result{}, err)
}

// fetchNvmeOfOSD retrieves the NvmeOfOSD instance by name and namespace.
func (r *ReconcileNvmeOfStorage) fetchNvmeOfStorage(request reconcile.Request) (*cephv1.NvmeOfStorage, error) {
	nvmeOfOSD := &cephv1.NvmeOfStorage{}
	err := r.client.Get(r.opManagerContext, request.NamespacedName, nvmeOfOSD)
	if err != nil {
		logger.Error(err, "unable to fetch NvmeOfStorage", "Request.Namespace", request.Namespace, "Request.Name", request.Name)
		return nil, err
	}
	return nvmeOfOSD, nil
}

// createNvmeOfOSD creates NvmeOfOSD CRs for each device in the NvmeOfStorage CR.
func (r *ReconcileNvmeOfStorage) createNvmeOfOSD(nvmeOfStorage *cephv1.NvmeOfStorage) error {
	for index, device := range nvmeOfStorage.Spec.Devices {
		osdName := nvmeOfStorage.Spec.Name + "-osd-" + strconv.Itoa(index)
		namespace := nvmeOfStorage.Namespace
		nvmeOfOSD := &cephv1.NvmeOfOSD{
			ObjectMeta: metav1.ObjectMeta{
				Name:      osdName,
				Namespace: namespace,
			},
			Spec: cephv1.NvmeOfOSDSpec{
				Name:              "osd-" + strconv.Itoa(index),
				NvmeOfStorageName: nvmeOfStorage.Spec.Name,
				IP:                nvmeOfStorage.Spec.IP,
				Port:              device.Port,
				SubNQN:            device.SubNQN,
				VNode:             "vnode_" + osdName,
				AttachNode:        device.AttachedNode,
			},
			Status: cephv1.NvmeOfOSDStatus{
				Status: "Creating",
			},
		}
		err := r.client.Create(r.opManagerContext, nvmeOfOSD)
		if err != nil {
			logger.Errorf("failed to create NvmeOfOSD %s. %+v", osdName, err)
			return err
		}
	}
	return nil
}

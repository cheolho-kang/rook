#!/usr/bin/env -S bash -e

ROOT_DIR=$(dirname $(dirname $(realpath $0))/../../../)

kubectl delete -f ${ROOT_DIR}/deploy/examples/cluster-minikube.yaml -f ${ROOT_DIR}/deploy/examples/crds.yaml -f ${ROOT_DIR}/deploy/examples/common.yaml -f ${ROOT_DIR}/deploy/examples/operator.yaml
kubectl create -f ${ROOT_DIR}/deploy/examples/crds.yaml -f ${ROOT_DIR}/deploy/examples/common.yaml -f ${ROOT_DIR}/deploy/examples/operator.yaml

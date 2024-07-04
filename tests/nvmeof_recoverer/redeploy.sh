#!/usr/bin/env -S bash -e

ROOT_DIR=$(dirname $(dirname $(realpath $0))/../../../)

kubectl delete -f ${ROOT_DIR}/deploy/examples/operator.yaml
# kubectl delete -f nvmeofstorage.yaml
kubectl create -f ${ROOT_DIR}/deploy/examples/operator.yaml

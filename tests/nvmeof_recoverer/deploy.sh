#!/usr/bin/env -S bash -e

ROOT_DIR=$(dirname $(dirname $(realpath $0))/../../../)

sed -i -e 's|image: rook/ceph:master|image: 192.168.100.13:5000/local/ceph-amd64:latest|' \
       -e 's|ROOK_LOG_LEVEL: "INFO"|ROOK_LOG_LEVEL: "DEBUG"|' ${ROOT_DIR}/deploy/examples/operator.yaml

kubectl create -f ${ROOT_DIR}/deploy/examples/crds.yaml -f ${ROOT_DIR}/deploy/examples/common.yaml -f ${ROOT_DIR}/deploy/examples/operator.yaml
kubectl create -f ${ROOT_DIR}/deploy/examples/toolbox.yaml

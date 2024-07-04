#!/usr/bin/env -S bash -e

ROOT_DIR=$(dirname $(dirname $(realpath $0))/../../../)
IP_ADDRESS=$(ip addr show | grep 'inet ' | grep '192.168.100.' | awk '{print $2}' | cut -d/ -f1)

cd $ROOT_DIR
make BUILD_REGISTRY=local
docker tag local/ceph-amd64 $IP_ADDRESS:5000/local/ceph-amd64
docker push $IP_ADDRESS:5000/local/ceph-amd64

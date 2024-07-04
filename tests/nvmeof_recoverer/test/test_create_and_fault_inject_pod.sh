#!/usr/bin/env -S bash -e
pwd="$(dirname "$(realpath "$0")")"

kubectl apply -f ${pwd}/../manifest/test_pod.yaml
kubectl patch pod test-ceph-pod --type='json' -p='[{"op": "replace", "path": "/spec/containers/0/command", "value":["exit","1"]}]' -n rook-ceph

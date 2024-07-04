#!/usr/bin/env -S bash -e

NAMESPACE="nvmeof-recoverer"
OSDID=$1

# kubectl scale replicaset -n ${NAMESPACE} $(kubectl get replicasets --namespace ${NAMESPACE} -l "ceph-osd-id=${OSDID}" -o jsonpath='{.items[0].metadata.name}') --replicas=0
kubectl patch deployment rook-ceph-osd-${OSDID} --type='json' -p='[{"op": "replace", "path": "/spec/template/spec/containers/0/command", "value":["exit","1"]}]' -n ${NAMESPACE}

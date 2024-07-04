#!/usr/bin/env -S bash -e

NAMESPACE="nvmeof-recoverer"

kubectl get nvmeofstorages nvmeofstorage-pbssd1 -n ${NAMESPACE} -o yaml >output/nvmeofstorage.yaml
kubectl get cephcluster ${NAMESPACE} -n ${NAMESPACE} -o yaml >output/cephcluster.yaml

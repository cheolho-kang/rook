#!/usr/bin/env -S bash -e
cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: nvme-connect-job
  namespace: default
spec:
  template:
    spec:
      containers:
      - name: nvmeof-connect
        image: quay.io/ceph/ceph:v18
        command: ["python3", "/scripts/nvme_connector.py"]
        env:
        - name: MODE
          value: "connect"
        - name: SUBNQN
          value: "nqn.2023-01.com.samsung.semiconductor:fc641c65-2548-4788-961f-a7ebaab3dc6a:0.2.S63UNG0T619224"
        - name: ADDRESS
          value: "192.168.100.14"
        - name: PORT
          value: "1152"
        - name: CONFIG_MAP_NAME
          value: "nvme-connect-result"
        - name: NAMESPACE
          value: "default"
        volumeMounts:
        - name: host-dev
          mountPath: /dev
        - name: scripts
          mountPath: /scripts
        securityContext:
          privileged: true
      restartPolicy: Never
      nodeSelector:
        kubernetes.io/hostname: minikube-m02
      volumes:
      - name: host-dev
        hostPath:
          path: /dev
      - name: scripts
        hostPath:
          path: /remote_home/chkang0912/gvm/pkgsets/go1.21.9/global/src/github.com/rook/rook/tests/nvmeof_recoverer
      hostNetwork: true
EOF

#!/usr/bin/env -S bash -e

minikube delete
./zapping_devices.sh

# minikube start --force --insecure-registry="192.168.100.13:5000"
minikube start --nodes 3 --force --insecure-registry="192.168.100.13:5000"

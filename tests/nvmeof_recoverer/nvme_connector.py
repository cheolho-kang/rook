#!/usr/bin/env python3

import json
import subprocess
import time


def get_nvme_devices():
    result = subprocess.run(['nvme', 'list', '-o', 'json'],
							stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    devices = json.loads(result.stdout)
    devices = {device['DevicePath'] for device in devices['Devices']}
    return devices

def connect_nvme(subnqn, ip_address, port):
    try:
        devices_before = get_nvme_devices()
        subprocess.run(['nvme', 'connect', '-t', 'tcp', '-n', subnqn,
						'-a', ip_address, '-s', port], check=True)
        time.sleep(1)
        devices_after = get_nvme_devices()
        new_devices = [device for device in devices_after if device not in devices_before]
        if new_devices:
            result = '\n'.join(new_devices)
            print('success:', result)
    except subprocess.CalledProcessError as e:
        print('failed:', e)


def disconnect_nvme(subnqn):

    try:
        result = subprocess.run(['nvme', 'disconnect', '-n', subnqn],
								stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        output = result.stdout.strip()
        print('success:', output)
    except subprocess.CalledProcessError as e:
        print('failed:', e)

mode = "connect"
address = "192.168.100.14"
port = "1152"
subnqn = "nqn.2023-01.com.samsung.semiconductor:fc641c65-2548-4788-961f-a7ebaab3dc6a:0.0.S63UNG0T619234"

if mode and subnqn and address and port:
    if mode == 'connect':
        connect_nvme(subnqn, address, port)
    elif mode == 'disconnect':
        disconnect_nvme(subnqn)

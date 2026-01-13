#!/bin/bash


#Install open-iscsi
sudo apt-get install open-iscsi

#Enable nfs kernel modlues on boot:
sudo tee /etc/modules-load.d/nfs.conf <<EOF
nfs
nfsd
lockd
sunrpc
EOF

sudo systemctl restart systemd-modules-load

#disable multipath service (might cause problems, thats why its getting disabled)
sudo systemctl stop multipathd
sudo systemctl disable multipathd
sudo systemctl mask multipathd
sudo systemctl stop multipathd.socket
sudo systemctl disable multipathd.socket
sudo systemctl mask multipathd.socket



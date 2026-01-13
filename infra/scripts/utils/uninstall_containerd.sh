#!/bin/bash

sudo systemctl stop containerd
sudo systemctl disable containerd
sudo apt purge -y containerd
sudo apt autoremove -y
sudo rm -f /etc/containerd/config.toml
sudo rm -rf /etc/containerd
sudo rm -rf /var/lib/containerd
sudo rm -rf /run/containerd

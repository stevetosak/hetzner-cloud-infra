#!/bin/bash
sudo kubeadm reset -f
sudo rm -rf /etc/kubernetes /var/lib/etcd /var/lib/kubelet/* /run/kubernetes /opt/cni/bin /etc/cni/net.d
sudo rm -rf ~/.kube/
sudo apt-mark unhold kubelet kubeadm kubectl
sudo apt purge -y kubelet kubeadm kubectl
sudo apt autoremove -y
sudo rm -f /etc/apt/sources.list.d/kubernetes.list
sudo rm -f /etc/apt/keyrings/kubernetes-apt-keyring.gpg
sudo apt update
sudo kubeadm reset -f
sudo rm -rf /etc/kubernetes /var/lib/etcd /var/lib/kubelet/* /run/kubernetes
sudo systemctl restart containerd
sudo rm -rf ~/.kube/

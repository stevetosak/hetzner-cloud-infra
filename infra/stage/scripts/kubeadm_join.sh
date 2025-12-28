#!/bin/bash
sudo kubeadm join 10.0.1.5:6443 --token xt1ipo.svcfbr78kz0ej2bl \
	--discovery-token-ca-cert-hash sha256:d2e997f839f50a407becca30f7aaca538ce336dd20c3c70620a61cb3d3b7d029
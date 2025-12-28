#!/bin/bash
kubectl create secret generic db-credentials -n authos --from-env-file=/home/stevetosak/Projects/Authos/secrets/k8s_secrets.env
kubectl create secret generic keystore -n authos --from-file=/etc/keystore/keystore.p12
kubectl create secret generic keystore-pass -n authos --from-literal=KEYSTORE_PASS="<>"
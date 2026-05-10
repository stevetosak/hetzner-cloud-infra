#!/bin/bash
kubectl create secret generic credentials -n wasteio \
  --from-literal=DB_USER="" \
  --from-literal=DB_PASS="" \
  --from-literal=JWT_SECRET=""

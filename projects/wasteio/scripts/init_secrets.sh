#!/bin/bash
kubectl create secret generic credentials -n wasteio \
  --from-literal=DB_USER="" \
  --from-literal=DB_PASS="" \
  --from-literal=JWT_SECRET="" \
  --from-literal=MQTT_PASSWORD=""

kubectl create secret generic wasteio-bin-agent-credentials -n wasteio \
  --from-literal=SIM_ADMIN_EMAIL="" \
  --from-literal=SIM_ADMIN_PASSWORD=""

#!/bin/bash

# Mark all workers for recreation
terraform taint 'hcloud_server.workers["k8s-wk1"]'
terraform taint 'hcloud_server.workers["k8s-wk2"]'
terraform taint 'hcloud_server.workers["k8s-wk3"]'

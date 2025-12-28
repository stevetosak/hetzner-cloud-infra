kubectl delete node k8swk1
kubectl delete node k8swk2
kubectl delete node k8swk3

terraform taint 'hcloud_server.workers["k8swk1"]'
terraform taint 'hcloud_server.workers["k8swk2"]'
terraform taint 'hcloud_server.workers["k8swk3"]'

terraform apply -var="allow_public_ssh=true"
# Write the output to a text file
terraform output -json worker_public_ips | jq -r '.[]' > "$HOME"/Projects/Authos/infra/stage/out/worker_ips.txt

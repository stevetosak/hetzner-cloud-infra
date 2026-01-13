#kubectl delete node k8swk1
#kubectl delete node k8swk2
#kubectl delete node k8swk3
#
#terraform taint 'hcloud_server.workers["k8swk1"]'
#terraform taint 'hcloud_server.workers["k8swk2"]'
#terraform taint 'hcloud_server.workers["k8swk3"]'
count=1

while read -r node; do
  kubectl delete node "$node" || true

  worker_tf_name="k8swk${count}"
  terraform taint "hcloud_server.workers[\"$worker_tf_name\"]"

  count=$((count + 1))
done < "$HOME/k8s/infra/stage/out/worker_names.txt"



terraform apply -var="allow_public_ssh=true" -var="node_suffix=$(date +%Y%m%d-%H%M%S)"

terraform output -json worker_public_ips | jq -r '.[]' > "$HOME"/k8s/infra/stage/out/worker_ips.txt
terraform output -json worker_names | jq -r '.[]' \
  > "$HOME/k8s/infra/stage/out/worker_names.txt"



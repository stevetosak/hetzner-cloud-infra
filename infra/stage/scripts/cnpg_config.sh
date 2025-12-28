#cnpg not being able to create cluster fix:

kubectl patch validatingwebhookconfiguration cnpg-validating-webhook-configuration -p '{"webhooks": [{"name": "vcluster.cnpg.io","failurePolicy": "Ignore"}]}'
validatingwebhookconfiguration.admissionregistration.k8s.io/cnpg-validating-webhook-configuration patched

kubectl patch mutatingwebhookconfiguration cnpg-mutating-webhook-configuration -p '{"webhooks": [{"name": "mcluster.cnpg.io","failurePolicy": "Ignore"}]}'
mutatingwebhookconfiguration.admissionregistration.k8s.io/cnpg-mutating-webhook-configuration patched
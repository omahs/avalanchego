# Bootstrap testing



## Bootstrap controller

### Primary Requirement

 - Run full sync and state sync bootstrap tests against mainnet and testnet

### Secondary requiremnts

 - Run tests in infra-managed kubernetes
   - Ensures sufficient resources (~2tb required per test)
   - Ensures metrics and logs will be collected by Datadog
 - Ensure that no more than one test will be run against a given image
 -

### TODO
 - accepts bootstrap configurations
   - network-id
   - sync-mode
 - kube configuration
   - in-cluster or not
 - start a wait loop (via kubernetes)
   - check image version
   - for each bootstrap config
     - if config.running -
       - continue
     - if config.last_version !=  version
       - start a new test run
       - pass a


StartJob(clientset, namespace, imageName, pvcSize, flags)
  - starts node
  - periodically checks if node pod is running and node is healthy
  - errors out on timeout
    - log result (use zap)
  - on healthy
    - log result
    - update last version
    - update the config to '

# Bootstrap testing



## Bootstrap controller

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

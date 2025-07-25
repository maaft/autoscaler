# DataCrunch Cluster Autoscaler Provider

This directory contains the DataCrunch cloud provider implementation for the Kubernetes cluster autoscaler. The DataCrunch provider enables automatic scaling of Kubernetes nodes on the DataCrunch cloud platform, with special support for GPU workloads and Multi-Instance GPU (MiG) configurations.

**Note**: This provider was built based on the Hetzner cloud provider implementation and adapted for DataCrunch's API and infrastructure requirements.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Prerequisites](#prerequisites)
- [Configuration](#configuration)
- [Deployment](#deployment)
- [Examples](#examples)
- [Advanced Features](#advanced-features)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)

## Overview

The DataCrunch provider integrates the Kubernetes cluster autoscaler with DataCrunch's cloud infrastructure, providing:

- **Automatic node scaling** based on pod resource demands
- **GPU-optimized instances** with support for NVIDIA MiG technology
- **Spot and on-demand instance support** for cost optimization
- **Dynamic startup script generation** for secure cluster joining
- **Flexible node pool configuration** with custom labels and taints

## Architecture

The provider consists of several key components:

### Core Components

- **`datacrunch_cloud_provider.go`**: Main cloud provider interface implementation
- **`datacrunch_manager.go`**: Manages DataCrunch API interactions and node group state
- **`datacrunch_node_group.go`**: Implements node group scaling operations
- **`datacrunch-go/`**: Go client library for DataCrunch API

### Optional Components

- **Caching layers**: Server type and instance caching for performance optimization

## Prerequisites

### DataCrunch Account Setup

1. **API Credentials**: Generate REST API credentials from the DataCrunch dashboard

   - Go to Keys page → Create REST API Credentials
   - Store client ID and secret securely

2. **SSH Keys**: Upload SSH public keys to DataCrunch for instance access

3. **Resources**: Ensure sufficient quota for desired instance types in target regions

### Kubernetes Cluster

- **Existing cluster**: A running Kubernetes cluster (control plane)
- **RBAC permissions**: Service account with appropriate cluster-autoscaler permissions
- **Container runtime**: NVIDIA container runtime for GPU workloads (e.g. gpu-operator)

## Configuration

### Environment Variables

The provider requires several environment variables:

```bash
# Required: DataCrunch API credentials
DATACRUNCH_CLIENT_ID="your-client-id"
DATACRUNCH_CLIENT_SECRET="your-client-secret"

# Required: Node pool configuration (choose one)
DATACRUNCH_CLUSTER_CONFIG_JSON='{"node_configs": {...}}'  # JSON string
DATACRUNCH_CLUSTER_CONFIG="base64-encoded-json"          # Base64 encoded
DATACRUNCH_CLUSTER_CONFIG_FILE="/path/to/config.json"    # File path

# Optional: Startup script configuration
DATACRUNCH_STARTUP_SCRIPT="#!/bin/bash\necho 'Hello World'"  # Global startup script
DATACRUNCH_STARTUP_SCRIPT_FILE="<path to file>"              # Is only read when DATACRUNCH_STARTUP_SCRIPT is empty
DATACRUNCH_DELETE_SCRIPTS_AFTER_BOOT="true"                  # Auto-delete scripts after execution
```

### Node Pool Configuration

Configure node pools via JSON configuration:

```json
{
  "node_configs": {
    "gpu-nodes": {
      "image_type": "ubuntu-24.04-cuda-12.8-open-docker",
      "ssh_key_ids": ["your-ssh-key-id"],
      "instance_option": "prefer_spot",
      "disk_size_gb": 100,
      "override_num_gpus": 7,
      "pricing_option": "dynamic",
      "taints": [
        {
          "key": "gpu-node",
          "effect": "NoSchedule"
        }
      ],
      "labels": {
        "nodepool": "gpu",
        "nvidia.com/mig.config": "all-1g.10gb"
      }
    }
  }
}
```

#### Configuration Options

| Field                   | Type     | Description                                                                           |
| ----------------------- | -------- | ------------------------------------------------------------------------------------- |
| `image_type`            | string   | DataCrunch image name (e.g., `ubuntu-24.04-cuda-12.8-open-docker`)                    |
| `ssh_key_ids`           | []string | List of SSH key IDs for instance access                                               |
| `instance_option`       | string   | Instance preference: `prefer_spot`, `prefer_on_demand`, `spot_only`, `on_demand_only` |
| `disk_size_gb`          | int      | OS disk size in GB                                                                    |
| `override_num_gpus`     | int      | Override GPU count (useful for MiG configurations)                                    |
| `pricing_option`        | string   | Pricing model: `dynamic` or `fixed` (on-demand only)                                  |
| `startup_script_base64` | string   | Base64-encoded startup script (takes precedence over `DATACRUNCH_STARTUP_SCRIPT`)     |
| `taints`                | []object | Kubernetes taints that created nodes will have                                        |
| `labels`                | map      | Labels that created nodes will have                                                   |

**Note**: It's your responsibility to make sure that override_num_gpus (if used), taints and labels are correct. This is usually done as part of your startup-script.

### Command Line Arguments

Configure the autoscaler with node group specifications:

```bash
--cloud-provider=datacrunch
--nodes=<min>:<max>:<instance-type>:<region>:<node-group-name>
```

Example:

```bash
--nodes=0:3:1A100.22V:FIN-01:gpu-nodes
```

This creates a node group named `gpu-nodes` with:

- Minimum 0 nodes, maximum 3 nodes
- Instance type `1A100.22V`
- Region `FIN-01`

## Deployment

Deploy the cluster autoscaler with DataCrunch provider configuration:

```yaml
# See examples/autoscaler.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cluster-autoscaler
spec:
  template:
    spec:
      containers:
        - name: cluster-autoscaler
          image: your-registry/cluster-autoscaler:latest
          command:
            - ./cluster-autoscaler
            - --cloud-provider=datacrunch
            - --nodes=0:3:1A100.22V:FIN-01:gpu-nodes
          env:
            - name: DATACRUNCH_CLIENT_ID
              valueFrom:
                secretKeyRef:
                  name: datacrunch-secrets
                  key: client-id
            - name: DATACRUNCH_CLIENT_SECRET
              valueFrom:
                secretKeyRef:
                  name: datacrunch-secrets
                  key: client-secret
            - name: DATACRUNCH_CLUSTER_CONFIG_JSON
              valueFrom:
                configMapKeyRef:
                  name: datacrunch-cluster-config
                  key: cluster_config
            - name: DATACRUNCH_STARTUP_SCRIPT
              valueFrom:
                configMapKeyRef:
                  name: startup-script
                  key: STARTUP_SCRIPT
            - name: DATACRUNCH_DELETE_SCRIPTS_AFTER_BOOT
              value: "true"
```

### Startup Script Configuration Options

You can configure startup scripts in three ways (in order of precedence):

1. **Per-nodepool scripts** (highest precedence): Set `startup_script_base64` in nodepool configuration
2. **Global startup script**: Set `DATACRUNCH_STARTUP_SCRIPT` or `DATACRUNCH_STARTUP_SCRIPT_FILE` environment variables. `DATACRUNCH_STARTUP_SCRIPT` takes precedence.
3. **No startup script**: Instances will boot with default image configuration

Example per-nodepool script configuration:

```json
{
  "node_configs": {
    "gpu-nodes": {
      "startup_script_base64": "IyEvYmluL2Jhc2gKZWNobyAnSGVsbG8gV29ybGQn",
      ...
    }
  }
}
```

## Examples

### Example Files

The `examples/` directory contains the following configuration files:

- **`autoscaler.yaml`**: Complete cluster autoscaler deployment
- **`startup-script.yaml`**: Example startup script ConfigMap
- **`clusterconfig.yaml`**: Node pool configuration example
- **`test-gpu.yaml`**: GPU workload for testing autoscaling

### Testing GPU Autoscaling

Deploy a GPU workload to trigger autoscaling. Make sure the taints match your nodepool configuration.

```yaml
# See examples/test-gpu.yaml
apiVersion: v1
kind: Pod
metadata:
  name: nvidia-smi-gpu-test
spec:
  runtimeClassName: nvidia
  tolerations:
    - key: "gpu-node"
      operator: "Exists"
      effect: "NoSchedule"
  containers:
    - name: nvidia-smi
      image: nvidia/cuda:12.2.0-base-ubuntu22.04
      resources:
        limits:
          nvidia.com/gpu: 1
      command: ["nvidia-smi"]
```

### Node Pool Configuration Examples

#### Pool for non-ephemeral workloads

```json
{
  "node_configs": {
    "on-demand-node-pool": {
      "image_type": "ubuntu-24.04-cuda-12.8-open-docker",
      "ssh_key_ids": ["your-ssh-key-id"],
      "instance_option": "on_demand_only",
      "pricing_option": "fixed",
      "disk_size_gb": 200,
      "labels": {
        "nodepool": "on-demand"
      }
    }
  }
}
```

```bash
--nodes=0:3:1A100.22V:FIN-01:on-demand-node-pool
```

#### Spot-Only Pool

```json
{
  "node_configs": {
    "spot-gpu": {
      "image_type": "ubuntu-24.04-cuda-12.8-open-docker",
      "ssh_key_ids": ["your-ssh-key-id"],
      "instance_option": "spot_only",
      "disk_size_gb": 200,
      "taints": [
        {
          "key": "spot-instance",
          "effect": "NoSchedule"
        }
      ],
      "labels": {
        "nodepool": "spot"
      }
    }
  }
}
```

```bash
--nodes=0:3:1A100.22V:FIN-01:spot-gpu
```

#### Fallback to On-Demand if Spot is not available

```json
{
  "node_configs": {
    "spot-gpu": {
      "image_type": "ubuntu-24.04-cuda-12.8-open-docker",
      "ssh_key_ids": ["your-ssh-key-id"],
      "instance_option": "prefer_spot",
      "disk_size_gb": 200,
      "taints": [
        {
          "key": "maybe-spot-instance",
          "effect": "NoSchedule"
        }
      ],
      "labels": {
        "nodepool": "maybe-spot"
      }
    }
  }
}
```

## Advanced Features

### Multi-Instance GPU (MiG) Support

The provider supports NVIDIA MiG technology for GPU workload isolation:

1. **Configuration**: Set `override_num_gpus` to match MiG profile
2. **Startup Script**: Configure MiG mode during instance initialization
3. **Labeling**: Use labels to indicate MiG configuration

Example MiG configuration:

```json
{
  "override_num_gpus": 7,
  "labels": {
    "nvidia.com/mig.config": "all-1g.10gb"
  }
}
```

The startup script handles MiG setup:

```bash
# unload processe that otherwise would block mig enablement. Alternatively: reboot after enabling mig
rmmod nvidia_drm
rmmod nvidia_modeset

# Enable MIG mode
nvidia-smi -i 0 -mig 1

# Create MiG instances
for j in {1..7}; do
  nvidia-smi mig -cgi 1g.10gb -C
done
```

**NOTE**: You are responsible that `override_num_gpus` match your actual configuration. The `"nvidia.com/mig.config": "all-1g.10gb"` is primarily used to make nvidias `mig-manager` (part of `gpu-operator`) happy.

### Startup Script Features

The DataCrunch provider includes several startup script features:

1. **Script Deletion**: Automatically deletes startup scripts after execution when `DATACRUNCH_DELETE_SCRIPTS_AFTER_BOOT="true"`
2. **Pre-script Injection**: Automatically prepends script deletion and instance ID retrieval logic
3. **Flexible Configuration**: Supports both global and per-nodepool startup scripts

#### ⚠️ Important: Provider ID Requirement

**Your startup script MUST set the node's provider ID for the cluster autoscaler to work correctly.** This is typically done by:

1. Setting the kubelet `--provider-id` flag during cluster join
2. Ensuring the provider ID format matches: `datacrunch://<instance-id>`

Without the correct provider ID, the autoscaler cannot associate Kubernetes nodes with DataCrunch instances, causing scaling failures.

#### Automatic Script Processing

The provider automatically:

1. **Prepends pre-script logic**: Adds script deletion and instance ID retrieval to your startup script
2. **Handles authentication**: Automatically injects DataCrunch API credentials
3. **Optional script deletion**: When `DATACRUNCH_DELETE_SCRIPTS_AFTER_BOOT="true"` is set, the startup script will be deleted from DataCrunch after execution
4. **Sets provider ID**: Makes the instance ID available as `$INSTANCE_ID` environment variable

Your startup script only needs to focus on cluster setup:

```bash
#!/bin/bash
set -euo pipefail

# The provider automatically prepends script deletion logic when DATACRUNCH_DELETE_SCRIPTS_AFTER_BOOT="true"
# Your script starts here with $INSTANCE_ID available

echo "Instance ID: $INSTANCE_ID"
PROVIDER_ID="datacrunch://$INSTANCE_ID"

# === YOUR CLUSTER SETUP SECTION ===
# Add your cluster join logic here
# Example: Install kubelet with provider ID

# Install your cluster agent (k3s, kubeadm, etc.)
# Make sure to set --provider-id=$PROVIDER_ID on kubelet

echo "Startup script completed successfully"
```

**Benefits of Automatic Script Processing:**

- **Enhanced Security**: Scripts containing sensitive information are automatically removed when enabled
- **Reduced Attack Surface**: No persistent scripts with cluster credentials on instances
- **Simplified Configuration**: No need to manually handle authentication and instance ID retrieval

#### Implementation Details

The provider uses a template file (embedded in `datacrunch_node_group.go`) to automatically prepend script deletion and instance ID retrieval logic to your startup scripts.

### Instance Type Selection

The provider supports intelligent instance type selection:

- **`prefer_spot`**: Try spot instances first, fallback to on-demand
- **`prefer_on_demand`**: Try on-demand first, fallback to spot
- **`spot_only`**: Only use spot instances
- **`on_demand_only`**: Only use on-demand instances

### Caching and Performance

The provider implements caching for optimal performance:

- **Server Type Cache**: Caches available instance types and regions
- **Server Cache**: Caches current instances to reduce API calls
- **Availability Checks**: Caches instance type availability per region

## API Client Limitations

### Current Implementation Status

The DataCrunch Go client (`datacrunch-go/`) provides core functionality for the cluster autoscaler but is **not a complete implementation** of the DataCrunch API. The client currently includes:

**Implemented Features:**

- **Instance Management**: Create, list, and delete instances
- **Startup Scripts**: Upload and manage startup scripts
- **SSH Key Operations**: Basic SSH key management
- **Volume Operations**: Volume lifecycle management
- **Authentication**: OAuth2 token handling

### Using the Official API

For operations not supported by the current client implementation, you can:

1. **Use the official DataCrunch API directly**: Refer to the [DataCrunch API Documentation](https://docs.datacrunch.io/api/) for complete endpoint reference
2. **Extend the client**: Add missing functionality by implementing additional methods in the appropriate client files
3. **Use alternative tools**: Utilize the official DataCrunch CLI or web interface for advanced operations

### Client Organization

The client is organized by functional areas:

- **`instances.go`**: Instance lifecycle operations
- **`volumes.go`**: Storage management
- **`scripts.go`**: Startup script operations
- **`sshkeys.go`**: SSH key management
- **`types.go`**: API request/response types
- **`client.go`**: Core authentication and HTTP client

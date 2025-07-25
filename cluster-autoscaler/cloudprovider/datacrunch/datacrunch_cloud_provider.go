/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package datacrunch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	autoscalerErrors "k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/autoscaler/cluster-autoscaler/utils/gpu"
	"k8s.io/klog/v2"
)

var _ cloudprovider.CloudProvider = (*DatacrunchCloudProvider)(nil)

const (
	// GPULabel is the label added to nodes with GPU resource.
	GPULabel                   = "datacrunch.io/gpu-node"
	providerIDPrefix           = "datacrunch://"
	nodeGroupLabel             = "datacrunch.io/node-group"
	datacrunchLabelNamespace   = "datacrunch.io"
	serverCreateTimeoutDefault = 5 * time.Minute
	serverRegisterTimeout      = 10 * time.Minute
	defaultPodAmountsLimit     = 110
	maxPlacementGroupSize      = 10
)

// DatacrunchCloudProvider implements CloudProvider interface.
type DatacrunchCloudProvider struct {
	manager         *datacrunchManager
	resourceLimiter *cloudprovider.ResourceLimiter
}

// Name returns name of the cloud provider.
func (d *DatacrunchCloudProvider) Name() string {
	return cloudprovider.DatacrunchProviderName
}

// NodeGroups returns all node groups configured for this cloud provider.
func (d *DatacrunchCloudProvider) NodeGroups() []cloudprovider.NodeGroup {
	groups := make([]cloudprovider.NodeGroup, 0, len(d.manager.nodeGroups))
	for groupId := range d.manager.nodeGroups {
		groups = append(groups, d.manager.nodeGroups[groupId])
	}
	return groups
}

// NodeGroupForNode returns the node group for the given node, nil if the node
// should not be processed by cluster autoscaler, or non-nil error if such
// occurred. Must be implemented.
func (d *DatacrunchCloudProvider) NodeGroupForNode(node *apiv1.Node) (cloudprovider.NodeGroup, error) {
	instance, err := d.manager.serverForNode(node)
	if err != nil {
		return nil, fmt.Errorf("failed to check if instance %s exists error: %v", node.Spec.ProviderID, err)
	}

	var groupId string
	if instance == nil {
		klog.V(3).Infof("failed to find datacrunch instance for node %s", node.Name)
		nodeGroupId, exists := node.Labels[nodeGroupLabel]
		if !exists {
			return nil, nil
		}
		groupId = nodeGroupId
	} else {
		// DataCrunch does not have labels, so you may need to adapt this logic if grouping is different
		groupId = instance.Description // Example: use Description as group
		if groupId == "" {
			return nil, nil
		}
	}

	group, exists := d.manager.nodeGroups[groupId]
	if !exists {
		return nil, nil
	}

	return group, nil
}

// HasInstance returns whether a given node has a corresponding instance in this cloud provider
func (d *DatacrunchCloudProvider) HasInstance(node *apiv1.Node) (bool, error) {
	instance, err := d.manager.serverForNode(node)
	if err != nil {
		return false, fmt.Errorf("failed to check if instance %s exists error: %v", node.Spec.ProviderID, err)
	}

	return instance != nil, nil
}

// Pricing returns pricing model for this cloud provider or error if not
// available. Implementation optional.
func (d *DatacrunchCloudProvider) Pricing() (cloudprovider.PricingModel, autoscalerErrors.AutoscalerError) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetAvailableMachineTypes get all machine types that can be requested from
// the cloud provider. Implementation optional.
func (d *DatacrunchCloudProvider) GetAvailableMachineTypes() ([]string, error) {
	serverTypes, err := d.manager.cachedServerType.getAllServerTypes()
	if err != nil {
		return nil, err
	}

	types := make([]string, 0, len(serverTypes))
	for _, server := range serverTypes {
		types = append(types, server.Name)
	}

	return types, nil
}

// NewNodeGroup builds a theoretical node group based on the node definition
// provided. The node group is not automatically created on the cloud provider
// side. The node group is not returned by NodeGroups() until it is created.
// Implementation optional.
func (d *DatacrunchCloudProvider) NewNodeGroup(
	machineType string,
	labels map[string]string,
	systemLabels map[string]string,
	taints []apiv1.Taint,
	extraResources map[string]resource.Quantity,
) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetResourceLimiter returns struct containing limits (max, min) for
// resources (cores, memory etc.).
func (d *DatacrunchCloudProvider) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	return d.resourceLimiter, nil
}

// GPULabel returns the label added to nodes with GPU resource.
func (d *DatacrunchCloudProvider) GPULabel() string {
	return GPULabel
}

// GetAvailableGPUTypes return all available GPU types cloud provider supports.
func (d *DatacrunchCloudProvider) GetAvailableGPUTypes() map[string]struct{} {
	return nil
}

// GetNodeGpuConfig returns the label, type and resource name for the GPU added to node. If node doesn't have
// any GPUs, it returns nil.
func (d *DatacrunchCloudProvider) GetNodeGpuConfig(node *apiv1.Node) *cloudprovider.GpuConfig {
	return gpu.GetNodeGPUFromCloudProvider(d, node)
}

// Cleanup cleans up open resources before the cloud provider is destroyed,
// i.e. go routines etc.
func (d *DatacrunchCloudProvider) Cleanup() error {
	return nil
}

// Refresh is called before every main loop and can be used to dynamically
// update cloud provider state. In particular the list of node groups returned
// by NodeGroups() can change as a result of CloudProvider.Refresh().
func (d *DatacrunchCloudProvider) Refresh() error {
	for _, group := range d.manager.nodeGroups {
		group.resetTargetSize(0)
	}
	return nil
}

// BuildDatacrunch builds the DataCrunch cloud provider.
func BuildDatacrunch(_ config.AutoscalingOptions, do cloudprovider.NodeGroupDiscoveryOptions, rl *cloudprovider.ResourceLimiter) cloudprovider.CloudProvider {
	manager, err := newManager()
	if err != nil {
		klog.Fatalf("Failed to create DataCrunch manager: %v", err)
	}

	provider, err := newDatacrunchCloudProvider(manager, rl)
	if err != nil {
		klog.Fatalf("Failed to create DataCrunch cloud provider: %v", err)
	}

	if len(manager.clusterConfig.NodeConfigs) == 0 {
		klog.Fatalf("No cluster config present provider: %v", err)
	}

	validNodePoolName := regexp.MustCompile(`^[a-z0-9A-Z]+[a-z0-9A-Z\-\.\_]*[a-z0-9A-Z]+$|^[a-z0-9A-Z]{1}$`)
	clusterUpdateLock := sync.Mutex{}
	for _, nodegroupSpec := range do.NodeGroupSpecs {
		spec, err := createNodePoolSpec(nodegroupSpec)
		if err != nil {
			klog.Fatalf("Failed to parse pool spec `%s` provider: %v", nodegroupSpec, err)
		}

		validNodePoolName.MatchString(spec.name)
		instances, err := manager.allServers(spec.name)
		if err != nil {
			klog.Fatalf("Failed to get instances for node pool %s error: %v", nodegroupSpec, err)
		}

		manager.nodeGroups[spec.name] = &datacrunchNodeGroup{
			manager:            manager,
			id:                 spec.name,
			minSize:            spec.minSize,
			maxSize:            spec.maxSize,
			instanceType:       spec.instanceType,
			region:             spec.region,
			targetSize:         len(instances),
			clusterUpdateMutex: &clusterUpdateLock,
		}
	}

	return provider
}

func createNodePoolSpec(groupSpec string) (*datacrunchNodeGroupSpec, error) {
	tokens := strings.SplitN(groupSpec, ":", 5)
	if len(tokens) != 5 {
		return nil, fmt.Errorf("expected format `<min-servers>:<max-servers>:<machine-type>:<region>:<name>` got %s", groupSpec)
	}

	definition := datacrunchNodeGroupSpec{
		instanceType: tokens[2],
		region:       tokens[3],
		name:         tokens[4],
	}
	if size, err := strconv.Atoi(tokens[0]); err == nil {
		definition.minSize = size
	} else {
		return nil, fmt.Errorf("failed to set min size: %s, expected integer", tokens[0])
	}

	if size, err := strconv.Atoi(tokens[1]); err == nil {
		definition.maxSize = size
	} else {
		return nil, fmt.Errorf("failed to set max size: %s, expected integer", tokens[1])
	}

	return &definition, nil
}

func newDatacrunchCloudProvider(manager *datacrunchManager, rl *cloudprovider.ResourceLimiter) (*DatacrunchCloudProvider, error) {
	return &DatacrunchCloudProvider{
		manager:         manager,
		resourceLimiter: rl,
	}, nil
}

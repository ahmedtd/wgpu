//go:build !(js && wasm)

// Copyright 2026 The GoGPU Authors
// SPDX-License-Identifier: MIT

package vulkan

import (
	"math"
	"unsafe"

	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu/hal"
	"github.com/gogpu/wgpu/hal/vulkan/vk"
)

// Device extensions required for gputypes.FeatureRayQuery.
//
// VK_KHR_buffer_device_address and VK_EXT_descriptor_indexing are also
// dependencies of VK_KHR_acceleration_structure, but both are core in
// Vulkan 1.2, which ray query support requires here (see queryRayTracingCaps).
const (
	extensionDeferredHostOperations = "VK_KHR_deferred_host_operations"
	extensionAccelerationStructure  = "VK_KHR_acceleration_structure"
	extensionRayQuery               = "VK_KHR_ray_query"
)

// rayQueryDeviceExtensions are the extensions Adapter.open enables when
// gputypes.FeatureRayQuery is requested.
var rayQueryDeviceExtensions = []string{
	extensionDeferredHostOperations,
	extensionAccelerationStructure,
	extensionRayQuery,
}

// maxShaderStagesPerPipeline mirrors Rust wgpu-hal's MAX_SHADER_STAGES_PER_PIPELINE,
// used to split per-set descriptor limits into per-stage limits.
const maxShaderStagesPerPipeline = 2

// rayTracingCaps records what a physical device supports for ray queries.
// Reference: Rust wgpu-hal PhysicalDeviceFeatures::{acceleration_structure,
// buffer_device_address, ray_query} and PhysicalDeviceProperties::acceleration_structure.
type rayTracingCaps struct {
	// rayQuery is true when the device can expose gputypes.FeatureRayQuery:
	// the extensions are present and accelerationStructure, rayQuery and
	// bufferDeviceAddress are all supported.
	rayQuery bool

	// asProperties holds VkPhysicalDeviceAccelerationStructurePropertiesKHR.
	// Only valid when rayQuery is true.
	asProperties vk.PhysicalDeviceAccelerationStructurePropertiesKHR
}

// enumerateDeviceExtensions returns the set of device extension names a
// physical device supports.
func enumerateDeviceExtensions(cmds *vk.Commands, physicalDevice vk.PhysicalDevice) map[string]struct{} {
	available := make(map[string]struct{})
	var count uint32
	if cmds.EnumerateDeviceExtensionProperties(physicalDevice, 0, &count, nil) != vk.Success || count == 0 {
		return available
	}
	props := make([]vk.ExtensionProperties, count)
	if cmds.EnumerateDeviceExtensionProperties(physicalDevice, 0, &count, &props[0]) != vk.Success {
		return available
	}
	for i := uint32(0); i < count && i < uint32(len(props)); i++ {
		available[cStringToGo(props[i].ExtensionName[:])] = struct{}{}
	}
	return available
}

// hasAllExtensions reports whether every name in want is in available.
func hasAllExtensions(available map[string]struct{}, want []string) bool {
	for _, name := range want {
		if _, ok := available[name]; !ok {
			return false
		}
	}
	return true
}

// queryRayTracingCaps detects ray query support for a physical device.
//
// Rust wgpu-hal reports EXPERIMENTAL_RAY_QUERY from extension presence alone.
// This also checks the feature bits that Adapter.open will enable, so a
// device that lists the extensions but can't enable them isn't advertised.
// Vulkan 1.2 is required so bufferDeviceAddress can be enabled through
// VkPhysicalDeviceVulkan12Features, which open already chains for timeline
// semaphores (the spec forbids also chaining the standalone BDA struct).
func queryRayTracingCaps(cmds *vk.Commands, physicalDevice vk.PhysicalDevice, apiVersion uint32, extensions map[string]struct{}) rayTracingCaps {
	var caps rayTracingCaps
	if apiVersion < vkMakeVersion(1, 2, 0) || !cmds.HasPhysicalDeviceFeatures2() {
		return caps
	}
	if !hasAllExtensions(extensions, rayQueryDeviceExtensions) {
		return caps
	}

	// Feature query: Vulkan12 -> AccelerationStructure -> RayQuery.
	rayQueryFeatures := vk.PhysicalDeviceRayQueryFeaturesKHR{
		SType: vk.StructureTypePhysicalDeviceRayQueryFeaturesKhr,
	}
	asFeatures := vk.PhysicalDeviceAccelerationStructureFeaturesKHR{
		SType: vk.StructureTypePhysicalDeviceAccelerationStructureFeaturesKhr,
		PNext: (*uintptr)(unsafe.Pointer(&rayQueryFeatures)),
	}
	vulkan12Features := vk.PhysicalDeviceVulkan12Features{
		SType: vk.StructureTypePhysicalDeviceVulkan12Features,
		PNext: (*uintptr)(unsafe.Pointer(&asFeatures)),
	}
	features2 := vk.PhysicalDeviceFeatures2{
		SType: vk.StructureTypePhysicalDeviceFeatures2,
		PNext: (*uintptr)(unsafe.Pointer(&vulkan12Features)),
	}
	cmds.GetPhysicalDeviceFeatures2(physicalDevice, &features2)

	if vulkan12Features.BufferDeviceAddress == 0 ||
		asFeatures.AccelerationStructure == 0 ||
		rayQueryFeatures.RayQuery == 0 {
		return caps
	}

	// Property query for limits and scratch alignment.
	caps.asProperties = vk.PhysicalDeviceAccelerationStructurePropertiesKHR{
		SType: vk.StructureTypePhysicalDeviceAccelerationStructurePropertiesKhr,
	}
	props2 := vk.PhysicalDeviceProperties2{
		SType: vk.StructureTypePhysicalDeviceProperties2,
		PNext: (*uintptr)(unsafe.Pointer(&caps.asProperties)),
	}
	cmds.GetPhysicalDeviceProperties2(physicalDevice, &props2)
	caps.asProperties.PNext = nil

	caps.rayQuery = true
	return caps
}

// features returns the gputypes features enabled by these capabilities.
// Reference: Rust adapter.rs PhysicalDeviceFeatures::to_wgpu (EXPERIMENTAL_RAY_QUERY).
func (c rayTracingCaps) features() gputypes.Features {
	if !c.rayQuery {
		return 0
	}
	return gputypes.Features(gputypes.FeatureRayQuery)
}

// applyLimits fills the acceleration structure limits.
// Reference: Rust adapter.rs PhysicalDeviceProperties::to_wgpu_limits.
func (c rayTracingCaps) applyLimits(limits *gputypes.Limits) {
	if !c.rayQuery {
		return
	}
	p := c.asProperties
	limits.MaxBlasGeometryCount = clampU64ToU32(p.MaxGeometryCount)
	limits.MaxBlasPrimitiveCount = clampU64ToU32(p.MaxPrimitiveCount)
	limits.MaxTlasInstanceCount = clampU64ToU32(p.MaxInstanceCount)
	limits.MaxAccelerationStructuresPerShaderStage = min(
		p.MaxPerStageDescriptorAccelerationStructures,
		p.MaxDescriptorSetAccelerationStructures/maxShaderStagesPerPipeline,
	)
	limits.MaxBuffersAndAccelerationStructuresPerShaderStage = math.MaxUint32
}

// applyAlignments fills the ray tracing alignments.
// Reference: Rust adapter.rs PhysicalDeviceProperties::to_hal_alignments.
func (c rayTracingCaps) applyAlignments(alignments *hal.Alignments) {
	alignments.RawTlasInstanceSize = 64 // sizeof(VkAccelerationStructureInstanceKHR)
	if c.rayQuery {
		alignments.RayTracingScratchBufferAlignment = c.asProperties.MinAccelerationStructureScratchOffsetAlignment
	}
}

func clampU64ToU32(v uint64) uint32 {
	if v > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}

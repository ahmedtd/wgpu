//go:build !(js && wasm)

// Copyright 2026 The GoGPU Authors
// SPDX-License-Identifier: MIT

package vulkan

import (
	"fmt"

	"github.com/gogpu/naga"
	"github.com/gogpu/naga/ir"
	"github.com/gogpu/naga/spirv"
)

// spirvOptions returns the naga SPIR-V backend options for Vulkan.
//
// naga.CompileWithOptions only forwards Version, Debug and BoundsCheckPolicies,
// so the backend options are built here instead. Reference: Rust wgpu-hal
// adapter.rs Adapter::open (naga_options), which sets
// ray_query_initialization_tracking and force_loop_bounding to true.
//
// Not yet matched (see docs/RAY-TRACING-PLAN.md task 2.9): Rust picks the SPIR-V
// version from the device API version and enables Restrict bounds checks.
func spirvOptions() spirv.Options {
	return spirv.Options{
		Version: spirv.Version1_3,
		// Guards rayQueryProceed/rayQueryGet* against use of an uninitialized or
		// finished ray query, which is undefined behavior in SPIR-V.
		RayQueryInitTracking: true,
		// Bounds every loop so a shader can't hang the GPU.
		ForceLoopBounding: true,
	}
}

// compileWGSLToSPIRV compiles WGSL to SPIR-V the way naga.Compile does
// (parse, lower, validate, apply override defaults), but with spirvOptions.
func compileWGSLToSPIRV(source string) ([]byte, error) {
	ast, err := naga.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}
	// LowerWithCapabilities validates with the same zero capability set that
	// naga.Compile uses (no f64/i64/f16).
	module, err := naga.LowerWithCapabilities(ast, source, ir.Capabilities(0))
	if err != nil {
		return nil, fmt.Errorf("lowering error: %w", err)
	}
	module, err = naga.ProcessOverrides(module, naga.OverrideOptions{})
	if err != nil {
		return nil, fmt.Errorf("pipeline constants: %w", err)
	}
	return naga.GenerateSPIRV(module, spirvOptions())
}

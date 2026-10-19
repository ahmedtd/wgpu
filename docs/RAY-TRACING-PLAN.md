# Ray Tracing Parity Plan (Vulkan first)

> **Branch:** `rt-work` in this repo (and `rt-work` in `../go-naga` for shader-side fixes)
> **Goal:** Bring the public `wgpu` API up to parity with Rust wgpu's
> `EXPERIMENTAL_RAY_QUERY` surface, so that ports of
> `rust-wgpu/examples/features/src/ray_traced_triangle` and
> `rust-wgpu/examples/features/src/ray_scene` run on the Vulkan backend and match
> the Rust reference screenshots.
> **Reference checkouts:** `../rust-wgpu` (commit `e084b66aa`), `../go-naga` (v0.20.3, `main`).
> **Last updated:** 2026-10-18 (Phases 0 and 2 complete; Phase 1 done except the naga release + `go.mod` bump in 1.4;
> 3.1's gputypes change made early, unreleased)

This document is the hand-off point between sessions. **Each session should pick
the first unchecked task, do it, tick the box, and add a line to the
[Session Log](#session-log).** If you find new gaps, add them as tasks in the
right phase instead of fixing them silently.

See also `docs/RAY-TRACING.md` (what exists at the HAL layer today).

---

## 1. Where we are today (research summary)

The HAL layer is largely written, but nothing above it is, and the Vulkan
backend can't actually run any of it yet.

| Layer | State | Notes |
|---|---|---|
| `gputypes` (v0.8.0, external module) | **Done on `../gputypes` `rt-work` (unreleased)**; the notes below describe v0.8.0 | Has `CreateBlasDescriptor`, `CreateTlasDescriptor`, `BlasGeometrySizeDescriptors`, `Blas{Triangle,AABB}GeometrySizeDescriptor`, AS flags/geometry flags/update mode/copy mode, `BufferUsageBlasInput/TlasInput/AccelerationStructureScratch/...Query`, AS limits, `FeatureRayQuery`, `BindGroupLayoutEntry.AccelerationStructure`. **Missing:** a `BindingResource` for acceleration structures (the interface has an unexported marker method, so only gputypes can add one) and an equivalent of `Limits::using_minimum_supported_acceleration_structure_values()`. |
| `hal/raytracing.go`, `hal/api.go`, `hal/command.go` | **Done** | Device: Create/Destroy AS, GetBuildSizes, GetDeviceAddress, TlasInstanceToBytes. Encoder: BuildAccelerationStructures, PlaceAccelerationStructureBarrier, CopyAccelerationStructure, ReadAccelerationStructureCompactSize. Matches wgpu-hal. `hal.Alignments` has `RawTlasInstanceSize` and `RayTracingScratchBufferAlignment`. |
| `hal/vulkan/raytracing.go` | **Works (Phase 2)** | `TestRayQuerySmoke` builds a BLAS + TLAS and traces rays from a compute shader on the RTX 5080 with validation layers clean. |
| `hal/vulkan/vk` bindings | **Fixed in Phase 2** | Procs are loaded. Two binding bugs were also fixed: the AS geometry union had 1-byte alignment, and `vk-gen` ignored `extnumber` (so 55 constants, including `IndexTypeNoneKhr`, were wrong). |
| `hal/software/raytracing.go` | CPU BVH only | The software *shader interpreter* doesn't support `ray_query`, so it can't run the example shaders. It's still useful for headless unit tests of build orchestration. |
| `internal/raytracing/` | **Done, not wired up** | `Blas`/`Tlas` state structs, `BuildContext` (scratch sizing, built-index), compaction state machine, validation, and a `DeviceContext` interface. `core/ray_tracing.go` only has the `deviceRTContext` adapter, and nothing calls it. |
| `core/` | **Missing** | No `core.Blas`/`core.Tlas` resources, no create/build/submit-time validation, no tracking. |
| `wgpu` (public API) | **Missing** | No `Blas`, `Tlas`, `TlasInstance`, `Device.CreateBlas/CreateTlas`, `CommandEncoder.BuildAccelerationStructures`, `BindGroupEntry` AS resource, or `Queue.CompactBlas`. |
| Rust FFI backend (`-tags rust`) | Not available | `go-webgpu/webgpu` v0.5.5 has no RT bindings. Stub the API with `ErrUnsupported`. |
| Browser backend | Not available | Not in the W3C spec. Stub the API with `ErrUnsupported`. |
| go-naga (WGSL → SPIR-V) | **Works (rt-work branch, unreleased)** | `enable wgpu_ray_query`, `acceleration_structure`, `ray_query`, `RayDesc`, `rayQuery*` builtins and SPIR-V `ray_query.go` helpers all exist. The `CompactTypes` panic (§1.2) is fixed on `../go-naga` branch `rt-work`, and both example shaders compile and pass `spirv-val --target-env vulkan1.2` (see Phase 1). |

### 1.1 Vulkan blockers (all confirmed by reading the code) — all FIXED in Phase 2

1. **RT extensions and features are never enabled.** `hal/vulkan/adapter.go:open()` only enables
   `VK_KHR_swapchain` (and optionally `VK_KHR_incremental_present`). It needs
   `VK_KHR_acceleration_structure`, `VK_KHR_deferred_host_operations`,
   `VK_KHR_buffer_device_address` (core in 1.2), and `VK_KHR_ray_query`, plus a
   feature chain: `bufferDeviceAddress` (in `PhysicalDeviceVulkan12Features`, which already exists for timeline semaphores),
   `PhysicalDeviceAccelerationStructureFeaturesKHR.accelerationStructure`, and
   `PhysicalDeviceRayQueryFeaturesKHR.rayQuery`. `Adapter.Open` also ignores the requested features (`_ gputypes.Features`).
   Rust reference: `wgpu-hal/src/vulkan/adapter.rs` (~lines 73–101, 189–196, 458–483, 965–980).
2. **`FeatureRayQuery` is never reported.** `featuresFromPhysicalDevice` (`hal/vulkan/api.go`)
   doesn't check for it, and the AS limits (`MaxBlas*`, `MaxTlasInstanceCount`, `MaxAccelerationStructuresPerShaderStage`)
   are never filled from `PhysicalDeviceAccelerationStructurePropertiesKHR`. `hal.Alignments.RayTracingScratchBufferAlignment`
   (`minAccelerationStructureScratchOffsetAlignment`) and `RawTlasInstanceSize` (64) are never set for Vulkan.
3. **RT function pointers are never loaded** (see the table above). `vkCmdBuildAccelerationStructuresKHR`,
   `vkCreate/DestroyAccelerationStructureKHR`, `vkGetAccelerationStructureBuildSizesKHR`,
   `vkGetAccelerationStructureDeviceAddressKHR`, `vkCmdCopyAccelerationStructureKHR`,
   `vkCmdWriteAccelerationStructuresPropertiesKHR`, and `vkGetBufferDeviceAddress` are all affected.
4. **Buffer usage mapping ignores RT usages.** `bufferUsageToVk` (`hal/vulkan/convert.go:15`) needs
   `BlasInput`/`TlasInput` → `ACCELERATION_STRUCTURE_BUILD_INPUT_READ_ONLY_BIT_KHR | SHADER_DEVICE_ADDRESS_BIT`,
   `AccelerationStructureScratch` → `STORAGE_BUFFER_BIT | SHADER_DEVICE_ADDRESS_BIT`, and
   `AccelerationStructureQuery` → `TRANSFER_DST`. (Rust: `wgpu-hal/src/vulkan/conv.rs` `map_buffer_usage`.)
5. **Device-address memory.** Any buffer with `SHADER_DEVICE_ADDRESS_BIT` must be allocated with
   `VkMemoryAllocateFlagsInfo{flags: DEVICE_ADDRESS_BIT}` chained into `vkAllocateMemory`
   (`hal/vulkan/memory/allocator.go:450`). That includes the AS backing buffer in `CreateAccelerationStructure`.
   The simplest approach is to enable it for all allocations when `bufferDeviceAddress` is on, as Rust's gpu-allocator config does.
6. **Bind groups can't hold an AS.** `CreateBindGroupLayout` (`hal/vulkan/device.go:938`) doesn't map
   `entry.AccelerationStructure` → `VK_DESCRIPTOR_TYPE_ACCELERATION_STRUCTURE_KHR`. The descriptor pool
   (`hal/vulkan/descriptor.go:228`) has no AS pool size. `updateDescriptorSet` (`device.go` ~line 1071)
   needs a case that chains `VkWriteDescriptorSetAccelerationStructureKHR` through `pNext`. All of the vk types and constants exist
   (`vk.DescriptorTypeAccelerationStructureKhr`, `vk.WriteDescriptorSetAccelerationStructureKHR`).
7. **Small correctness issue in `BuildAccelerationStructures`** (`hal/vulkan/raytracing.go:523`). When `dstAS == nil`,
   the `continue` runs *after* appending to `geometriesStorage`/`rangesStorage`, so those slices fall out of step with
   `geometryInfos`. Move the `dstAS` check before the appends. Also check that the Go-heap structs whose addresses are passed
   through goffi (`PGeometries`, `rangePtrs`) stay alive until the call returns. Use `runtime.KeepAlive` or pinning, matching
   how other multi-pointer calls in `hal/vulkan` do it.
8. **Pipelines must declare SPIR-V capability support.** Check that `core/spirv_capabilities.go` and the Vulkan pipeline creation
   accept `RayQueryKHR` / `SPV_KHR_ray_query` when the feature is enabled.

### 1.2 go-naga blocker (confirmed and root-caused) — FIXED in Phase 1.1

`nagac ../rust-wgpu/examples/features/src/ray_traced_triangle/shader.wgsl` panics with
`index out of range [15] with length 15` in `spirv/internal/codegen/backend.go:604 emitType`, called from
`writeRayQueryGetIntersection`.

**Root cause:** in `ir/compact.go` `CompactTypes`, the *removal* path (step 3, about lines 260–325) remaps
constants, overrides, globals, functions, entry points, global expressions, and `TypeUseOrder`, but **not
`module.SpecialTypes`**. Only the later *reordering* path (about line 597) remaps them. When any type ahead of
`RayIntersection` is removed, `SpecialTypes.RayIntersection` goes stale. The upstream snapshot `ray-query.wgsl` happens
not to trigger it.

**Fix (verified locally, then reverted):** right before the "Remap and filter TypeUseOrder" block in the
removal path, remap `SpecialTypes.RayIntersection`, `ExternalTextureParams`, and
`ExternalTextureTransferFunction` through `remap`. With that change, `ray_traced_triangle/shader.wgsl`,
`ray_traced_triangle/blit.wgsl`, and `ray_scene/shader.wgsl` all compile. The SPIR-V output **has not been run
through `spirv-val`** yet, because the tool isn't installed on the dev machine. Install it (Vulkan SDK / `spirv-tools`)
or diff the output against Rust naga (`cargo run -p naga-cli -- shader.wgsl out.spvasm` in `../rust-wgpu`).

go-wgpu pins `github.com/gogpu/naga v0.19.0`, but the `../go-naga` checkout is at v0.20.3. During development, use a
`go.work` (not committed) or a temporary `replace github.com/gogpu/naga => ../go-naga`, then bump the pin once a
go-naga release carries the fix.

### 1.3 Rust public API we're matching

From `rust-wgpu/wgpu/src/api/{blas,tlas,device,command_encoder,queue,bind_group}.rs`:

```rust
// Device
fn create_blas(&self, desc: &CreateBlasDescriptor, sizes: BlasGeometrySizeDescriptors) -> Blas;
fn create_tlas(&self, desc: &CreateTlasDescriptor) -> Tlas;
// CommandEncoder
fn build_acceleration_structures<'a>(&mut self,
    blas: impl IntoIterator<Item=&'a BlasBuildEntry<'a>>,
    tlas: impl IntoIterator<Item=&'a Tlas>);
unsafe fn mark_acceleration_structures_built(...);          // low priority
// Queue
fn compact_blas(&self, blas: &Blas) -> Blas;
// Blas
fn handle(&self) -> Option<u64>;
fn prepare_compaction_async(&self, cb: impl FnOnce(Result<(), BlasAsyncError>));
fn ready_for_compaction(&self) -> bool;
// Tlas  (owns Vec<Option<TlasInstance>> of len max_instances; Index/IndexMut)
fn get(&self) -> &[Option<TlasInstance>];
fn get_mut_slice(&mut self, r: Range<usize>) -> Option<&mut [Option<TlasInstance>]>;
fn get_mut_single(&mut self, i: usize) -> Option<&mut Option<TlasInstance>>;
fn as_binding(&self) -> BindingResource;
// TlasInstance { blas, pub transform: [f32;12] (3x4 row-major), pub custom_data: u32 (24 bits), pub mask: u8 }
fn TlasInstance::new(blas: &Blas, transform: [f32;12], custom_data: u32, mask: u8) -> Self;
// BlasTriangleGeometry { size: &BlasTriangleGeometrySizeDescriptor, vertex_buffer, first_vertex,
//   vertex_stride, index_buffer: Option, first_index: Option<u32>, transform_buffer: Option,
//   transform_buffer_offset: Option<u64> }
// BlasAabbGeometry { size: &BlasAABBGeometrySizeDescriptor, stride, aabb_buffer, primitive_offset }
// BlasGeometries::{TriangleGeometries(Vec<..>), AabbGeometries(Vec<..>)}
// BlasBuildEntry { blas: &Blas, geometry: BlasGeometries }
// BindingResource::AccelerationStructure(&Tlas), ::AccelerationStructureArray(&[&Tlas])
```

How core works (`rust-wgpu/wgpu-core/src/command/ray_tracing.rs`, `device/ray_tracing.rs`, `ray_tracing.rs`):

- `create_blas` checks the feature, validates the size descriptors (vertex format, index format/count consistency,
  primitive and geometry limits), calls `get_acceleration_structure_build_sizes` with buffer-less entries, creates the
  raw AS at `acceleration_structure_size`, gets the device address as `handle`, and keeps the size descriptors for
  build-time validation.
- `create_tlas` also creates a persistent **instance buffer** (`max_instances * raw_tlas_instance_size`, usage
  `COPY_DST | TLAS_INPUT`).
- `build_acceleration_structures`:
  - In `iter_blas`, check each geometry against the creation-time size descriptor, check that buffers have `BLAS_INPUT`
    usage, and gather buffer barriers to `BOTTOM_LEVEL_ACCELERATION_STRUCTURE_INPUT`.
  - Sum the scratch sizes, each aligned to `ray_tracing_scratch_buffer_alignment`.
  - For each TLAS, serialize the instances with `tlas_instance_to_bytes` into one staging blob (custom_data must be
    below 2^24, plus the vertex-return flag check) and record the BLAS dependencies.
  - Allocate one shared scratch buffer, sized to max(blas total, tlas total).
  - Build the BLASes, then barrier `BUILD_OUTPUT → BUILD_INPUT`.
  - Copy staging → each TLAS instance buffer (with transitions).
  - Build the TLASes, then barrier `BUILD_OUTPUT → SHADER_INPUT`.
  - Push the scratch and staging buffers to temp resources freed after the submission.
  - Record `AsAction::Build`.
- Bind group creation with a TLAS records `AsAction::UseTlas`.
- At submit, `validate_acceleration_structure_actions` assigns monotonically increasing `built_index`es. It errors if
  a used TLAS is unbuilt, if a dependency BLAS is unbuilt, or if a BLAS is newer than the TLAS that uses it. Builds
  reset the BLAS compaction state to Idle.

---

## 2. Target Go API

Follow the existing Go conventions here. Creators return `(T, error)`. Resources have `Release()`. `BindGroupEntry` is
a struct with one pointer field per resource kind. Optional values are pointers. Rust `Option<TlasInstance>` becomes
`*TlasInstance`, with `nil` meaning empty. Re-export the descriptor types from `gputypes` the same way `types.go`
already does for other types.

```go
// types.go — aliases
type (
    CreateBlasDescriptor               = gputypes.CreateBlasDescriptor
    CreateTlasDescriptor               = gputypes.CreateTlasDescriptor
    BlasGeometrySizeDescriptors        = gputypes.BlasGeometrySizeDescriptors
    BlasTriangleGeometrySizeDescriptor = gputypes.BlasTriangleGeometrySizeDescriptor
    BlasAABBGeometrySizeDescriptor     = gputypes.BlasAABBGeometrySizeDescriptor
    AccelerationStructureFlags         = gputypes.AccelerationStructureFlags
    AccelerationStructureGeometryFlags = gputypes.AccelerationStructureGeometryFlags
    AccelerationStructureUpdateMode    = gputypes.AccelerationStructureUpdateMode
    AccelerationStructureBindingLayout = gputypes.AccelerationStructureBindingLayout
)
// + const re-exports: FeatureRayQuery, BufferUsageBlasInput, BufferUsageTlasInput, AS flag consts…

// blas.go (shared) + blas_native.go / blas_rust.go / blas_browser.go
type Blas struct { /* core *core.Blas; device *Device; released … */ }
func (b *Blas) Handle() (uint64, bool)
func (b *Blas) PrepareCompactionAsync(cb func(error))
func (b *Blas) ReadyForCompaction() bool
func (b *Blas) Release()

type TlasInstance struct {
    Blas       *Blas
    Transform  [12]float32 // 3x4 row-major affine
    CustomData uint32      // low 24 bits only
    Mask       uint8
}
func NewTlasInstance(blas *Blas, transform [12]float32, customData uint32, mask uint8) *TlasInstance

type BlasTriangleGeometry struct {
    Size                  *BlasTriangleGeometrySizeDescriptor
    VertexBuffer          *Buffer
    FirstVertex           uint32
    VertexStride          uint64
    IndexBuffer           *Buffer  // optional
    FirstIndex            *uint32  // required iff IndexBuffer != nil
    TransformBuffer       *Buffer  // optional
    TransformBufferOffset *uint64  // required iff TransformBuffer != nil
}
type BlasAabbGeometry struct {
    Size            *BlasAABBGeometrySizeDescriptor
    Stride          uint64
    AabbBuffer      *Buffer
    PrimitiveOffset uint32
}
// Discriminated union, same shape as gputypes.BlasGeometrySizeDescriptors.
type BlasGeometries struct {
    Triangles []BlasTriangleGeometry
    AABBs     []BlasAabbGeometry
}
type BlasBuildEntry struct {
    Blas     *Blas
    Geometry BlasGeometries
}

// tlas.go
type Tlas struct { /* core *core.Tlas; instances []*TlasInstance; lowestUnmodified uint32 */ }
func (t *Tlas) MaxInstances() uint32
func (t *Tlas) Instances() []*TlasInstance        // read view (Rust get())
func (t *Tlas) SetInstance(i int, inst *TlasInstance) // Rust IndexMut / get_mut_single
func (t *Tlas) InstancesMut(lo, hi int) []*TlasInstance // Rust get_mut_slice; marks modified
func (t *Tlas) Release()

// device_*.go
func (d *Device) CreateBlas(desc *CreateBlasDescriptor, sizes BlasGeometrySizeDescriptors) (*Blas, error)
func (d *Device) CreateTlas(desc *CreateTlasDescriptor) (*Tlas, error)

// encoder_*.go
func (e *CommandEncoder) BuildAccelerationStructures(blas []BlasBuildEntry, tlas []*Tlas)

// queue_*.go
func (q *Queue) CompactBlas(blas *Blas) (*Blas, error)

// descriptor.go
type BindGroupEntry struct {
    // ...existing fields...
    AccelerationStructure *Tlas // binding_type AccelerationStructure
}

// Limits helper (wgpu or gputypes): returns limits with Rust's minimum AS values
func LimitsWithMinimumAccelerationStructureValues(l gputypes.Limits) gputypes.Limits
```

Deliberately deferred, to match Rust's lower-priority surface: `AccelerationStructureArray` binding
(needs `FeatureASBindingArray`), `mark_acceleration_structures_built`, `PreferUpdate` (Rust only logs a warning and
rebuilds), `as_hal`, and ray-tracing pipelines (`FeatureRayTracingPipelines`, which isn't part of the target examples).

---

## 3. Phased task list

Tasks are ordered so that each phase ends with something testable. Do **not** reorder Phase 0. Writing the examples
first pins down the API.

### Phase 0 — Examples first (API contract) — DONE 2026-10-18

- [x] **0.1** `examples/ray-traced-triangle/` ports `ray_traced_triangle`. The shaders and screenshot are copied
  verbatim and embedded with `//go:embed`. It renders one 1024×768 frame at t=0, headless, into an
  **`Rgba8UnormSrgb`** target. That's what the Rust test harness uses, since `Example::SRGB` defaults to true.
- [x] **0.2** `examples/ray-scene/` ports `ray_scene`. The models are in `examples/ray-scene/models/`.
  `obj.go` reproduces the grouping rules of the `obj` 0.10 crate (`o`/`usemtl`/`f`/EOF) and Rust's `load_model`.
  It's untagged and unit-tested: `go test ./examples/ray-scene` gives Ferris 4 geometries and cube.obj 2.
  The bind group layout is explicit, as planned.
- [x] **0.3** Golden comparison: `rtexample.ComparePNG` checks against the embedded `screenshot.png`. It uses
  `-threshold` (default 0.02; negative disables) rather than the `-compare` flag in the original wording. Both
  examples compare against `screenshot.png`, because that's what the Rust test uses on every backend.
  `screenshot-vulkan.png` is kept for reference. **Caveat:** Rust takes the mean of an NVIDIA FLIP error map.
  We use a plain mean absolute RGB difference, which is probably stricter. If an output looks right but fails
  narrowly, consider porting FLIP or loosening the threshold.
- [x] **0.4** Build tags: each example's `main.go` is `//go:build rtwip`, and a `stub.go` (`!rtwip`) prints
  "not implemented" and exits. This keeps `go build ./...`, `-tags rust`, and wasm builds green. **Tasks 3.8 and 3.9
  must delete `stub.go` and the `rtwip` tag.**

How Phase 0 was verified: the default build and all three backend configurations compile. `go test ./examples/...`
passes. With a temporary shim in package `wgpu` declaring exactly the §2 signatures (since deleted),
`go build -tags rtwip` and `go vet -tags rtwip` passed for both examples. So **the examples are the API contract**.
Implement §2 so that `go build -tags rtwip ./examples/ray-scene ./examples/ray-traced-triangle` compiles.
Without the shim, every error is a missing §2 symbol.

Shared helpers are in `examples/internal/rtexample/`, all untagged and tested:
- glam-equivalent matrix math: `LookAtRH`, `PerspectiveRH`, `Inverse`, `Rows3x4` (the TLAS transform layout),
  `RotationTranslationEulerXYZ`.
- `InitDevice(features, limits)`. `GOGPU_GRAPHICS_API` selects the backend and `GOGPU_DEBUG=1` turns on validation layers.
- `Target`: the render target plus readback.
- `CreateBufferInit`: the `create_buffer_init` equivalent, using `MappedAtCreation`.
- PNG writing and comparison.

API surface the examples actually use, as a checklist for Phase 3:
`wgpu.{Blas, Tlas, TlasInstance, NewTlasInstance, BlasBuildEntry, BlasGeometries, BlasTriangleGeometry,
CreateBlasDescriptor, CreateTlasDescriptor, BlasGeometrySizeDescriptors, BlasTriangleGeometrySizeDescriptor,
BufferUsageBlasInput, LimitsWithMinimumAccelerationStructureValues}`, `Device.CreateBlas`, `Device.CreateTlas`,
`CommandEncoder.BuildAccelerationStructures(blas []BlasBuildEntry, tlas []*Tlas)` (either argument may be nil),
`Tlas.SetInstance`, `Tlas.Instances`, `Blas.Release`, `Tlas.Release`, and `BindGroupEntry.AccelerationStructure`.

Gaps found while porting (not ray tracing specific; fix when convenient):
- [ ] **0.5** `wgpu.Texture` has no `Width()`/`Height()`/`Size()`, but Rust's `Texture::width()` exists.
  `ray-traced-triangle` keeps the dispatch size in the `Example` struct instead. If you add them, switch the
  dispatch back to `storageTexture.Width()/8`.
- [ ] **0.6** The examples depend on `MappedAtCreation` writes through `MappedRange.BytesMut()` + `Unmap`
  working for `BLAS_INPUT`/`STORAGE`/`UNIFORM` buffers on Vulkan. That path hasn't been exercised by these
  examples yet. Check it during 2.8 or 3.8.
- [ ] **0.7** Lint the example code. `golangci-lint` **is** installed now (`~/go/bin`). Without `rtwip`,
  `golangci-lint run ./examples/...` reports 6 issues in the RT example code (2 errcheck, 2 gocognit, 1 gosec in
  `rtexample/headless.go:188`, 1 nestif in `ray-scene/obj.go`). These aren't fixed yet. The `rtwip` files can't be
  linted until Phase 3 makes them compile.

### Phase 1 — go-naga fixes (in `../go-naga`, branch `rt-work`)

All go-naga changes are **uncommitted** in `../go-naga` on branch `rt-work`. `go test ./...` there passes with
`NAGA_REQUIRE_TOOLS=spirv-val,spirv-dis`.

- [x] **1.1** Fixed in `ir/compact.go`, matching Rust's `trace_special_types` / `adjust_special_types`. Special types
  are now marked used, so an anonymous one survives compaction, and they're remapped in the removal path.
  Regression test: `ir/compact_test.go` `TestCompactTypes_RemapsSpecialTypes`, which fails without the fix.
- [x] **1.2** Added `ray-traced-triangle`, `ray-traced-triangle-blit`, and `ray-scene` to `snapshot/testdata/own/in/`.
  The harness now accepts an optional `<name>.toml` next to an own shader (`rustParamsPath`); the two RT shaders use
  `ray-query.toml`'s parameters (no GLSL target). Rust references were generated at the vendored corpus commit
  `14328f49` and live in `snapshot/testdata/rust-derived/out/`, with the manifest rewritten.
  Results: **IR matches Rust exactly**, both before and after compaction (`TestIRReference`, `TestIRReferenceCompact`).
  SPIR-V matches structurally (`compareStructural` only compares counts). MSL and HLSL are recorded `bug` deviations,
  for the same unported upstream commits as the corpus shader `ray-query`: MSL `f6d9a1db` / `a03be8d2` (+ `74827c41`
  for compute) and HLSL `c97fe3e7`. Neither matters for Vulkan.
  While comparing, I also ported two SPIR-V fixes from upstream `55534165` into `spirv/internal/codegen/ray_query.go`.
  First, the intersection helper's triangle block (barycentrics/front_face) branched on "not none" instead of
  "is triangle". Second, `rayQueryProceed` now skips `OpRayQueryProceedKHR` after traversal finishes, since calling it
  again is UB in SPIR-V. The third part of that commit (the Terminate cache key) doesn't apply, because go-naga doesn't
  implement `rayQueryTerminate` at all (upstream `7721a3e0`).
- [x] **1.3** All three shaders pass `spirv-val --target-env vulkan1.2` with init tracking on and off, at SPIR-V 1.3
  (go-wgpu's current `naga.Compile` default) and 1.4 (what the Rust corpus uses for ray queries). Nothing needed
  fixing. **Caveat:** at SPIR-V **1.5+**, every shader (including the non-RT blit) fails SPIRV-Tools 2026.1's new
  `VUID-StandaloneSpirv-None-10684`, an `Offset` decoration on a struct used in Function storage. **Rust naga at
  `14328f49` fails it identically**, so it's upstream behavior, not a go-naga deviation. It only matters if task 2.9
  makes go-wgpu pick SPIR-V 1.5 the way Rust does for Vulkan 1.2+ devices.
- [ ] **1.4** `go.work` (`use . ../go-naga`) created and excluded through `.git/info/exclude`, so it isn't
  committed. With it, `go build ./...`, `-tags rust`, and wasm all build, and `go test ./...` passes. Same result with
  `GOWORK=off`, so the v0.19.0 → v0.20.3 naga jump didn't regress anything. **Remaining:** commit and release the
  go-naga changes, then bump `github.com/gogpu/naga` in `go.mod` and drop `go.work`.
  **Since Phase 2, `GOWORK=off` no longer builds.** `hal/vulkan/shader_compile.go` calls `naga.ProcessOverrides`
  (not in v0.19.0), and `hal/vulkan/device.go` uses `gputypes.AccelerationStructureBinding` (not in v0.8.0).
  `go.work` is now `use (. ../go-naga ../gputypes)`. Both modules need a release and a `go.mod` bump.
- [ ] **1.5** (found in 1.3; optional, needed for Phase 5.5) Port the remaining upstream ray-query commits to go-naga:
  MSL `f6d9a1db` (intersection queries) and `a03be8d2` (safety), HLSL `c97fe3e7` (safety), and SPIR-V `7721a3e0`
  (`rayQueryTerminate`). The `rust_deviations_test.go` entries for `ray-query*` and the new example shaders will
  then start failing as "no longer a difference", which is the signal to delete them.

### Phase 2 — Vulkan HAL enablement (`hal/vulkan`) — DONE 2026-10-18 (except 2.10)

- [x] **2.1** Done. `LoadDevice` loads `vkGetBufferDeviceAddress` and the 7 `*AccelerationStructure*KHR` procs the
  backend uses. `Commands.HasAccelerationStructure()` checks them, and `Adapter.open` fails if they're missing when
  ray query is requested. Also fixed: `SigVoidHandleHandlePtrPtrPtr` (`vkGetAccelerationStructureBuildSizesKHR`)
  declared the `buildType` enum as u64, so goffi read 8 bytes from a 4-byte value. It's u32 now.
- [x] **2.2** Done in `hal/vulkan/raytracing_caps.go`. `FeatureRayQuery` is reported when the device is Vulkan 1.2+,
  has `VK_KHR_{deferred_host_operations,acceleration_structure,ray_query}`, **and** reports the
  `bufferDeviceAddress`/`accelerationStructure`/`rayQuery` feature bits. Rust only checks extensions. AS limits come
  from `PhysicalDeviceAccelerationStructurePropertiesKHR`, as in Rust's `to_wgpu_limits`. `RawTlasInstanceSize = 64`
  is set always, and the scratch alignment when supported. Device extensions are now enumerated once, at
  enumeration, and stored on `Adapter.extensions`.
- [x] **2.3** Done. `Open` passes the requested features through; it errors if ray query is requested but
  unsupported. It enables the three extensions (BDA is core in 1.2, so its extension isn't needed) and chains
  `Vulkan12{bufferDeviceAddress} → AccelerationStructure → RayQuery`. The bufferDeviceAddress bit goes in the
  existing Vulkan12 struct because the spec forbids also chaining the standalone BDA struct.
- [x] **2.4** Done. `bufferUsageToVk` matches Rust `map_buffer_usage`. `memory.AllocatorConfig.BufferDeviceAddress`
  chains `VkMemoryAllocateFlagsInfo{DEVICE_ADDRESS}` into every `vkAllocateMemory` when ray query is enabled,
  matching gpu-allocator.
- [x] **2.5** Done, which required doing 3.1's gputypes part early (see 3.1). Layout entries map to
  `ACCELERATION_STRUCTURE_KHR` (with an error if the device lacks ray query). `DescriptorCounts.AccelerationStructures`
  adds an AS pool size only to pools created for AS layouts, as is done for dynamic buffers. `updateDescriptorSet`
  chains `VkWriteDescriptorSetAccelerationStructureKHR`.
  **Also fixed a non-RT bug found here:** `updateDescriptorSet` wrote every `TextureViewBinding` as
  `SAMPLED_IMAGE`/`SHADER_READ_ONLY_OPTIMAL`, even for storage textures. It now uses `STORAGE_IMAGE`/`GENERAL`
  when the layout says so. `ray-traced-triangle` writes to a storage texture, so it needs this.
- [x] **2.6** Done. The `dstAS == nil` check now runs before the appends, and the build/size-query slices are
  `runtime.KeepAlive`d past the FFI call.
  **Also found and fixed:** `vk.AccelerationStructureGeometryDataKHR` was `[64]byte`, so it had 1-byte alignment.
  That put `VkAccelerationStructureGeometryKHR.geometry` at offset 20 instead of 24, which would corrupt every
  build. It's `[8]uint64` now, in both `cmd/vk-gen` and `types_gen.go`. `vk/layout_raytracing_test.go` pins the
  C layout of all the RT structs. And `vk-gen` ignored `<enum extnumber=...>`, so `IndexTypeNoneKhr` was
  1000150000 instead of 1000165000, which the validation layers rejected. Regenerating `const_gen.go` corrected 55
  constants. `IndexTypeNoneKhr` is the only one the code uses. The generator otherwise reproduces the checked-in
  files exactly.
- [x] **2.7** Done. `core/track.BufferUses` has `BlasInput`/`TlasInput`/`AccelerationStructureScratch`/
  `AccelerationStructureQuery`. Scratch and query are write (exclusive) states, as in Rust. `ToBufferUsage` maps
  them. `bufferUsageToAccessAndStage` matches Rust `map_buffer_usage_to_barrier`. Nothing in core uses the new states
  yet; that's 3.4. Also removed `RAY_TRACING_SHADER_BIT_KHR` from the AS `SHADER_INPUT` barrier, since it's invalid
  without `VK_KHR_ray_tracing_pipeline`.
- [x] **2.9** (found in Phase 1) **Done for the ray-query part.** `hal/vulkan/shader_compile.go` runs
  Parse → LowerWithCapabilities → ProcessOverrides → `GenerateSPIRV` with `RayQueryInitTracking: true` and
  `ForceLoopBounding: true`. Everything else is unchanged (SPIR-V 1.3, zero capabilities, no bounds checks).
  compute-sum/copy/particles and triangle-headless still pass on Vulkan, and the three example shaders pass
  `spirv-val`. The rest moved to 2.10. Original text: `hal/vulkan/device.go:1243` calls
  `naga.Compile`, which goes through `naga.CompileWithOptions`. That function builds a bare `spirv.Options` with only
  Version/Debug/BoundsCheckPolicies, so it **drops `RayQueryInitTracking`** (and `ForceLoopBounding` /
  `UseStorageInputOutput16`), even though `spirv.DefaultOptions()` sets them. Rust's Vulkan HAL
  (`adapter.rs` ~2900–3000) uses `ray_query_initialization_tracking: true` (unless the shader module is created
  trusted), `force_loop_bounding: true`, Restrict bounds checks, a `RayQueryKHR` capability when the feature is
  enabled, and a SPIR-V version from the device API version (1.3 for Vulkan 1.1, 1.5 for 1.2, 1.6 for 1.3+, at least
  1.4 with `VK_KHR_spirv_1_4`). Either expose these in `naga.CompileOptions`, or have hal/vulkan call
  Parse/Lower/Validate/ProcessOverrides/`GenerateSPIRV` with `spirv.DefaultOptions()` plus overrides. See 1.3's
  SPIR-V 1.5 caveat before raising the version. This isn't needed for the examples to render correctly; it's a
  safety/parity item.
- [ ] **2.10** (split from 2.9) Remaining shader-option parity, which affects **all** shaders, not just RT:
  Restrict bounds checks (Index always; Buffer/ImageLoad unless robustBufferAccess2/robustImageAccess), a SPIR-V
  version from the device API version (see 1.3's 1.5+ caveat), and `UseStorageInputOutput16` gated on
  `SHADER_F16` like Rust. go-naga emits the `RayQueryKHR` capability itself, so nothing is needed for it.
- [x] **2.8** Done: `hal/vulkan/raytracing_integration_test.go` `TestRayQuerySmoke`. It enables validation layers and
  fails on any validation error. It builds a one-triangle BLAS and a one-instance TLAS (shared scratch buffer, TLAS
  region at the reported alignment), then runs a ray-query compute shader that writes kind, custom data, and t for 4
  rays (2 hits, 2 misses). It passes on the RTX 5080, and skips when no adapter has `FeatureRayQuery`. It caught the
  union alignment, `IndexTypeNoneKhr`, and (in the test itself) cleanup-order bugs. Original text: It should create a BLAS
  with one triangle, a TLAS with one instance, and run a compute shader with a ray query that writes hit/miss to a
  storage buffer, then read the result back. This exercises everything in Phase 2 before core exists.
  (The dev box has an RTX 5080 with `nvidia_icd.json`. lavapipe has no RT support.)

### Phase 3 — gputypes, core, and public API

- [ ] **3.1** **gputypes**: code done, release pending. `../gputypes` was cloned at v0.8.0 onto branch `rt-work`
  (committed there, not pushed or released). It adds `AccelerationStructureBinding{AccelerationStructure uintptr}` (`BindingResource`, carrying
  the HAL AS `NativeHandle()`), `Limits.UsingMinimumSupportedAccelerationStructureValues()`, and
  `Limits.UsingAccelerationStructureValues(other)`, with tests. It's wired in through `go.work`.
  **Remaining:** commit, release, and bump `go.mod`. `wgpu.LimitsWithMinimumAccelerationStructureValues` (§2) can
  wrap the new method.
- [ ] **3.2** **core resources:** add `core.Blas` and `core.Tlas` in `core/resource.go` (or a new
  `core/ray_tracing_resource.go`). Embed or own the `internal/raytracing.Blas/Tlas` state. Hold the HAL AS in a
  `Snatchable`, with `ResourceRef` refcounting like `core.Buffer`. `Tlas` owns its instance buffer.
  Register them with the destroy queue so they're freed after GPU completion.
- [ ] **3.3** **core create:** add `Device.CreateBlas` and `Device.CreateTlas`, porting
  `wgpu-core/src/device/ray_tracing.rs` (validation, size query, raw create, device address handle, instance
  buffer). Errors go in `core/error.go` as `CreateBlasError` and `CreateTlasError`, mirroring `wgpu-core/src/ray_tracing.rs`.
- [ ] **3.4** **core build:** add `CommandEncoder.BuildAccelerationStructures`, porting `build_acceleration_structures`
  and `iter_blas` (§1.3) on top of `internal/raytracing.BuildContext`. The scratch buffer needs usage
  `AccelerationStructureScratch`. Stage instance bytes through the existing staging belt or pending-writes mechanism
  (`staging_belt.go`, `pending_writes.go`). Push the temp buffers so they're released after the submission completes.
  Record the AS actions on the encoder.
- [ ] **3.5** **bind groups:** add `BindGroupEntry.AccelerationStructure`. In `toHAL`, emit the gputypes AS binding.
  Check the layout entry type and the `FeatureRayQuery` gate. Record `UseTlas` on any encoder that binds the group,
  or keep the TLAS list on the bind group and collect it at submit. Track the TLAS ref for lifetime.
- [ ] **3.6** **submit-time validation:** in `queue_native.go` Submit, port `validate_acceleration_structure_actions`
  (built_index assignment, unbuilt/newer-BLAS errors, compaction-state reset).
- [ ] **3.7** **public wrappers:** add `blas.go`, `tlas.go`, `*_native.go` methods, plus `_rust.go` and `_browser.go`
  stubs returning `ErrUnsupported`, so that all three build configurations compile:
  `go build ./...`, `go build -tags rust ./...`, `GOOS=js GOARCH=wasm go build .`.
- [ ] **3.8** Remove any `rtwip` tag from the examples. `ray-traced-triangle` should match its golden image on Vulkan.
- [ ] **3.9** Get `ray-scene` matching its golden image on Vulkan (it needs multiple BLASes, multi-geometry BLASes, and a per-frame TLAS rebuild).

### Phase 4 — Validation and test parity

- [ ] **4.1** Port the relevant Rust tests from `rust-wgpu/tests/tests/wgpu-gpu/ray_tracing/` (`as_build.rs`,
  `as_create.rs`, `as_use_after_free.rs`, `scene/`, `shader.rs`, `limits.rs`) as Go tests. Run them on Vulkan where
  available. Run the validation-only ones against the noop or software HAL.
- [ ] **4.2** Run `golangci-lint run --timeout=5m` and `go test ./...` across all build tags.
- [ ] **4.3** Update `docs/RAY-TRACING.md` (public API, examples), `CHANGELOG.md`, and `AGENTS.md`.

### Phase 5 — Beyond the examples (optional, after parity on the two examples)

- [ ] **5.1** BLAS compaction: `Blas.PrepareCompactionAsync`, `ReadyForCompaction`, `Queue.CompactBlas`
  (`internal/raytracing/compaction.go` already has the state machine).
  Rust example: `examples/features/src/ray_cube_compute` and the tests `as_build.rs` compaction cases.
- [ ] **5.2** AABB geometry. Port `examples/features/src/ray_aabb_compute`.
- [ ] **5.3** More Rust examples: `ray_cube_compute`, `ray_cube_fragment`, `ray_cube_normals` (vertex return,
  `FeatureRayHitVertexReturn`), `ray_shadows`.
- [ ] **5.4** `AccelerationStructureArray` binding (`FeatureASBindingArray`).
- [ ] **5.5** DX12 and Metal: the same core path should mostly just work. Verify the bind group and feature-reporting
  plumbing in those HALs.
- [ ] **5.6** Ray-query support in the software backend's shader interpreter, so the examples can run in CI without a GPU.

---

## 4. Testing notes

- **Hardware:** Vulkan RT needs a real driver (NVIDIA, RADV, or ANV). lavapipe doesn't support
  `VK_KHR_acceleration_structure`. Skip GPU tests cleanly when `FeatureRayQuery` isn't in `adapter.Features()`.
- **Tools:** `vulkan-tools` (`vulkaninfo`), `spirv-tools` (`spirv-val`, `spirv-dis`, SPIRV-Tools 2026.1), and
  `vulkan-validationlayers` are **installed** as of Phase 1 (apt). `vulkaninfo` sees the RTX 5080 (NVIDIA, Vulkan 1.4.329)
  and llvmpipe. `spirv-cross` is **not** installed. Rust naga's snapshot driver needs it for `.spvasm.glsl` side files,
  so the Phase 1 references were generated with a local patch that skips it (scratch worktree only). Run the examples with validation layers on (create the instance with
  `Flags: gputypes.InstanceFlagsDebug`; see `hal/vulkan/api.go:83`) and treat any validation message as a bug.
- **Reference output:** run the Rust examples for comparison with
  `cargo run --bin wgpu-examples ray_traced_triangle` from `../rust-wgpu`. The Rust GPU tests live in
  `../rust-wgpu/tests/tests/wgpu-gpu/ray_tracing/` (including `as_aabb.rs`).

## 5. Open questions

1. **gputypes ownership.** Can we land changes in `github.com/gogpu/gputypes` directly, or do we need a temporary
   local type in `hal` until upstream accepts them? Because of the unexported `BindingResource` marker, a
   gputypes change is the only clean option.
2. **Auto layout.** Should native get `layout: nil` + `GetBindGroupLayout` (derived from naga reflection) as part of
   this work? It isn't required for RT. This plan uses explicit layouts in the examples.
3. **Windowed examples.** The Rust examples are windowed (winit). This repo's examples are headless, and windowing lives
   in `gogpu/gogpu`. The plan makes them headless with golden-image comparison. Windowed versions could live in the gogpu repo later.

---

## Session Log

| Date | Session summary |
|---|---|
| 2026-10-18 | Research + this plan. Created branch `rt-work`. Found and root-caused the go-naga `CompactTypes` SpecialTypes remap bug (fix verified, then reverted; tracked as task 1.1). Found Vulkan RT blockers (§1.1). No code changes yet. |
| 2026-10-18 | Phase 0 done: `examples/ray-traced-triangle`, `examples/ray-scene` (behind `rtwip`), shared `examples/internal/rtexample`. Examples type-check against a temporary §2 shim. Next: Phase 1 (go-naga). |
| 2026-10-18 | Phase 1 (go-naga, uncommitted on `../go-naga` `rt-work`): fixed the `CompactTypes` SpecialTypes bug (1.1) and ported two SPIR-V ray-query fixes from upstream `55534165`. Added the example shaders to the own corpus with optional per-shader TOML and Rust-derived references; IR matches Rust exactly, and MSL/HLSL deviations are recorded (1.2). `spirv-val --target-env vulkan1.2` is clean at SPIR-V 1.3/1.4 (1.3). go-wgpu uses `go.work` → `../go-naga`, and builds and tests clean (1.4, pending naga release). New tasks: 1.5 (port remaining MSL/HLSL/terminate commits), 2.9 (Vulkan shader compile options parity, notably init tracking). Next: release naga + bump `go.mod`, then Phase 2. |
| 2026-10-18 | Phase 2 (Vulkan HAL) done: RT procs loaded; `FeatureRayQuery`, limits, and alignments reported; extensions and the feature chain enabled on request; RT buffer usages and device-address memory; AS bind group descriptors; build-loop desync and lifetimes fixed; AS buffer barrier states; ray query init tracking. Bugs found and fixed: AS geometry union alignment, `vk-gen` `extnumber` (55 constants), build-sizes FFI signature, and storage-image descriptors written as sampled images. `TestRayQuerySmoke` passes on the RTX 5080 with validation clean. Did 3.1's gputypes code early (`../gputypes` `rt-work`, committed locally), so `GOWORK=off` builds are broken until the naga and gputypes releases. New task 2.10. Next: Phase 3 (3.2 core resources). |

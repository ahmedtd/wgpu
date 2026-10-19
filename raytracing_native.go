//go:build !rust && !(js && wasm)

package wgpu

import (
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"

	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu/core"
	"github.com/gogpu/wgpu/hal"
)

// asCleanupRef holds what a runtime.AddCleanup callback needs to drop an
// acceleration structure's ownership reference. It must not reference the
// Blas or Tlas itself.
type asCleanupRef struct {
	kind     string
	label    string
	released *atomic.Bool
	drop     func()
}

func registerASCleanup[T any](obj *T, ref asCleanupRef) runtime.Cleanup {
	return runtime.AddCleanup(obj, func(ref asCleanupRef) {
		if !ref.released.CompareAndSwap(false, true) {
			return
		}
		slog.Warn("wgpu: "+ref.kind+" released by GC (missing explicit Release)", "label", ref.label)
		ref.drop()
	}, ref)
}

// Blas is a bottom-level acceleration structure: geometry (triangles or
// AABBs) that TLAS instances place in a scene. Experimental; requires
// FeatureRayQuery.
//
// Matches Rust wgpu's Blas.
type Blas struct {
	core     *core.Blas
	device   *Device
	released *atomic.Bool
	cleanup  runtime.Cleanup
}

// Handle returns the BLAS's device address, as written into TLAS instance
// data. The boolean is false if the backend has no such handle.
//
// Matches Rust wgpu's Blas::handle.
func (b *Blas) Handle() (uint64, bool) {
	if b == nil || b.core == nil {
		return 0, false
	}
	return b.core.Handle(), true
}

// Release drops the application's reference to the BLAS. The acceleration
// structure is destroyed once no in-flight command buffer and no TLAS
// references it anymore.
func (b *Blas) Release() {
	if b == nil || b.released == nil || !b.released.CompareAndSwap(false, true) {
		return
	}
	b.cleanup.Stop()
	b.core.Ref.Drop()
}

// Tlas is a top-level acceleration structure: a set of BLAS instances that
// shaders trace rays against. It holds MaxInstances instance slots; nil slots
// are empty and skipped when the TLAS is built. Experimental; requires
// FeatureRayQuery.
//
// Changes to the instances take effect at the next
// CommandEncoder.BuildAccelerationStructures that includes the TLAS.
//
// Tlas is not safe for concurrent use.
//
// Matches Rust wgpu's Tlas.
type Tlas struct {
	core     *core.Tlas
	device   *Device
	released *atomic.Bool
	cleanup  runtime.Cleanup

	instances []*TlasInstance
	// slotRefs[i] is the BLAS whose reference the TLAS holds for slot i, so
	// a BLAS stays alive while an instance slot points at it (in Rust, each
	// TlasInstance holds an Arc<Blas>). Slots changed through InstancesMut
	// are reconciled at the next build.
	slotRefs []*core.Blas
}

// MaxInstances returns the number of instance slots.
func (t *Tlas) MaxInstances() uint32 {
	return t.core.MaxInstances()
}

// Instances returns the instance slots. The slice must not be modified; use
// SetInstance or InstancesMut.
//
// Matches Rust wgpu's Tlas::get.
func (t *Tlas) Instances() []*TlasInstance {
	return t.instances
}

// SetInstance sets instance slot i (nil empties it). It panics if i is out
// of range, like indexing a slice.
//
// Matches Rust wgpu's Tlas IndexMut / get_mut_single.
func (t *Tlas) SetInstance(i int, instance *TlasInstance) {
	t.instances[i] = instance
	t.retainSlot(i)
}

// InstancesMut returns slots [lo, hi) for modification, or nil if the range
// is out of bounds.
//
// Matches Rust wgpu's Tlas::get_mut_slice.
func (t *Tlas) InstancesMut(lo, hi int) []*TlasInstance {
	if lo < 0 || hi > len(t.instances) || lo > hi {
		return nil
	}
	return t.instances[lo:hi:hi]
}

// Release drops the application's reference to the TLAS and to the BLASes
// its instance slots hold. The acceleration structure is destroyed once no
// in-flight command buffer or bind group references it anymore.
func (t *Tlas) Release() {
	if t == nil || t.released == nil || !t.released.CompareAndSwap(false, true) {
		return
	}
	t.cleanup.Stop()
	t.dropSlotRefs()
	t.core.Ref.Drop()
}

func (t *Tlas) dropSlotRefs() {
	for i, held := range t.slotRefs {
		if held != nil {
			held.Ref.Drop()
			t.slotRefs[i] = nil
		}
	}
}

// retainSlot moves slot i's held BLAS reference to the BLAS it currently
// points at. Reports whether the slot's BLAS is still alive.
func (t *Tlas) retainSlot(i int) bool {
	var want *core.Blas
	if inst := t.instances[i]; inst != nil && inst.Blas != nil {
		want = inst.Blas.core
	}
	held := t.slotRefs[i]
	if held == want {
		return want == nil || !want.IsDestroyed()
	}
	if want != nil {
		if want.IsDestroyed() {
			return false
		}
		want.Ref.Clone()
	}
	if held != nil {
		held.Ref.Drop()
	}
	t.slotRefs[i] = want
	return true
}

// CreateBlas creates a bottom-level acceleration structure sized for the
// given geometry. It must be built with CommandEncoder.BuildAccelerationStructures
// before a TLAS referencing it is used. Requires FeatureRayQuery.
//
// Matches Rust wgpu's Device::create_blas.
func (d *Device) CreateBlas(desc *CreateBlasDescriptor, sizes BlasGeometrySizeDescriptors) (*Blas, error) {
	if d.released.Load() {
		return nil, ErrReleased
	}
	coreBlas, err := d.core.CreateBlas(desc, sizes)
	if err != nil {
		return nil, err
	}
	b := &Blas{core: coreBlas, device: d, released: new(atomic.Bool)}
	b.cleanup = registerASCleanup(b, asCleanupRef{
		kind: "Blas", label: coreBlas.Label(), released: b.released, drop: coreBlas.Ref.Drop,
	})
	return b, nil
}

// CreateTlas creates a top-level acceleration structure with
// desc.MaxInstances empty instance slots. Requires FeatureRayQuery.
//
// Matches Rust wgpu's Device::create_tlas.
func (d *Device) CreateTlas(desc *CreateTlasDescriptor) (*Tlas, error) {
	if d.released.Load() {
		return nil, ErrReleased
	}
	coreTlas, err := d.core.CreateTlas(desc)
	if err != nil {
		return nil, err
	}
	n := coreTlas.MaxInstances()
	t := &Tlas{
		core:      coreTlas,
		device:    d,
		released:  new(atomic.Bool),
		instances: make([]*TlasInstance, n),
		slotRefs:  make([]*core.Blas, n),
	}
	// The cleanup drops the slot refs too; it captures the slice, not t.
	slotRefs := t.slotRefs
	t.cleanup = registerASCleanup(t, asCleanupRef{
		kind: "Tlas", label: coreTlas.Label(), released: t.released,
		drop: func() {
			for _, held := range slotRefs {
				if held != nil {
					held.Ref.Drop()
				}
			}
			coreTlas.Ref.Drop()
		},
	})
	return t, nil
}

// BuildAccelerationStructures builds the given BLASes and then the given
// TLASes from their current instances. Either argument may be nil.
//
// Errors are deferred to Finish, like other encoder commands. Whether the
// BLASes the TLAS instances reference are built is checked at Submit.
//
// Matches Rust wgpu's CommandEncoder::build_acceleration_structures.
func (e *CommandEncoder) BuildAccelerationStructures(blas []BlasBuildEntry, tlas []*Tlas) {
	if e.released {
		return
	}
	fail := func(err error) {
		e.setError(fmt.Errorf("wgpu: CommandEncoder.BuildAccelerationStructures: %w", err))
	}

	var buffers []*Buffer
	useBuffer := func(buf *Buffer) (*core.Buffer, error) {
		if buf == nil {
			return nil, nil //nolint:nilnil // optional buffer absent
		}
		if buf.released == nil || buf.released.Load() || buf.core == nil {
			return nil, fmt.Errorf("buffer %q is released: %w", buf.Label(), ErrReleased)
		}
		buffers = append(buffers, buf)
		return buf.core, nil
	}

	blasEntries := make([]core.BlasBuildEntry, len(blas))
	for i := range blas {
		entry, err := convertBlasBuildEntry(&blas[i], useBuffer)
		if err != nil {
			fail(err)
			return
		}
		blasEntries[i] = entry
	}

	tlasPackages := make([]core.TlasPackage, len(tlas))
	for i, t := range tlas {
		pkg, err := t.buildPackage()
		if err != nil {
			fail(err)
			return
		}
		tlasPackages[i] = pkg
	}

	if err := e.core.BuildAccelerationStructures(blasEntries, tlasPackages); err != nil {
		fail(err)
		return
	}

	// Keep everything the build reads or writes alive until the GPU is done.
	for i := range blasEntries {
		e.trackRef(blasEntries[i].Blas.Ref)
	}
	for i := range tlasPackages {
		e.trackRef(tlasPackages[i].Tlas.Ref)
		for _, inst := range tlasPackages[i].Instances {
			e.trackRef(inst.Blas.Ref)
		}
	}
	for _, buf := range buffers {
		e.trackRef(buf.core.Ref)
		e.trackBuffer(buf)
	}
}

func convertBlasBuildEntry(entry *BlasBuildEntry, useBuffer func(*Buffer) (*core.Buffer, error)) (core.BlasBuildEntry, error) {
	b := entry.Blas
	if b == nil {
		return core.BlasBuildEntry{}, fmt.Errorf("BLAS entry has no BLAS")
	}
	if b.released == nil || b.released.Load() {
		return core.BlasBuildEntry{}, fmt.Errorf("BLAS %q is released: %w", b.core.Label(), ErrReleased)
	}
	out := core.BlasBuildEntry{Blas: b.core}
	if entry.Geometry.Triangles != nil {
		out.Triangles = make([]core.BlasTriangleGeometry, len(entry.Geometry.Triangles))
		for i := range entry.Geometry.Triangles {
			g := &entry.Geometry.Triangles[i]
			vertex, err := useBuffer(g.VertexBuffer)
			if err != nil {
				return core.BlasBuildEntry{}, err
			}
			index, err := useBuffer(g.IndexBuffer)
			if err != nil {
				return core.BlasBuildEntry{}, err
			}
			transform, err := useBuffer(g.TransformBuffer)
			if err != nil {
				return core.BlasBuildEntry{}, err
			}
			out.Triangles[i] = core.BlasTriangleGeometry{
				Size:                  g.Size,
				VertexBuffer:          vertex,
				FirstVertex:           g.FirstVertex,
				VertexStride:          g.VertexStride,
				IndexBuffer:           index,
				FirstIndex:            g.FirstIndex,
				TransformBuffer:       transform,
				TransformBufferOffset: g.TransformBufferOffset,
			}
		}
	}
	if entry.Geometry.AABBs != nil {
		out.AABBs = make([]core.BlasAabbGeometry, len(entry.Geometry.AABBs))
		for i := range entry.Geometry.AABBs {
			g := &entry.Geometry.AABBs[i]
			aabb, err := useBuffer(g.AabbBuffer)
			if err != nil {
				return core.BlasBuildEntry{}, err
			}
			out.AABBs[i] = core.BlasAabbGeometry{
				Size:            g.Size,
				Stride:          g.Stride,
				AabbBuffer:      aabb,
				PrimitiveOffset: g.PrimitiveOffset,
			}
		}
	}
	return out, nil
}

// buildPackage reconciles the slot references and converts the instances
// for a build.
func (t *Tlas) buildPackage() (core.TlasPackage, error) {
	if t == nil {
		return core.TlasPackage{}, fmt.Errorf("TLAS is nil")
	}
	if t.released == nil || t.released.Load() {
		return core.TlasPackage{}, fmt.Errorf("TLAS %q is released: %w", t.core.Label(), ErrReleased)
	}
	pkg := core.TlasPackage{Tlas: t.core, Instances: make([]*core.TlasInstance, 0, len(t.instances))}
	for i, inst := range t.instances {
		if !t.retainSlot(i) {
			return core.TlasPackage{}, fmt.Errorf("TLAS %q instance %d references a released BLAS: %w",
				t.core.Label(), i, ErrReleased)
		}
		if inst == nil {
			continue
		}
		if inst.Blas == nil {
			return core.TlasPackage{}, fmt.Errorf("TLAS %q instance %d has no BLAS", t.core.Label(), i)
		}
		pkg.Instances = append(pkg.Instances, &core.TlasInstance{
			Blas:       inst.Blas.core,
			Transform:  inst.Transform,
			CustomData: inst.CustomData,
			Mask:       inst.Mask,
		})
	}
	return pkg, nil
}

// tlasLayoutMismatch reports whether a bind group entry's acceleration
// structure binding disagrees with its layout entry.
func tlasLayoutMismatch(entry *BindGroupEntry, layout *gputypes.BindGroupLayoutEntry) bool {
	return (entry.AccelerationStructure != nil) != (layout.AccelerationStructure != nil)
}

// halAccelerationStructure returns the TLAS's HAL acceleration structure, or
// nil if it is destroyed.
func (t *Tlas) halAccelerationStructure() hal.AccelerationStructure {
	if t == nil || t.core == nil || t.device == nil {
		return nil
	}
	guard := t.device.core.SnatchLock().Read()
	defer guard.Release()
	return t.core.Raw(guard)
}

//go:build !rust && !(js && wasm)

package wgpu

import (
	"testing"

	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu/core"
	"github.com/gogpu/wgpu/core/track"
)

// TestCreateTextureRegistersUninitializedState pins that a new texture enters
// the device tracker as uninitialized, so its first use transitions it out of
// that state (Rust create_texture inserts TextureUses::UNINITIALIZED).
func TestCreateTextureRegistersUninitializedState(t *testing.T) {
	t.Parallel()
	_, _, device := newTestDeviceWithTracker(t)
	defer device.Release()

	tex, _ := newUsageScopeTexture(t, device, "fresh", TextureUsageTextureBinding)
	idx := tex.coreTexture.TrackingData().Index()
	if got := device.core.Tracker().Textures().GetUsage(idx); got != track.TextureUsesUninitialized {
		t.Errorf("device state of a new texture = %v, want Uninitialized", got)
	}
}

// TestBindGroupUsagesFromLayoutEntries pins the usage each binding type
// contributes to pass usage scopes.
func TestBindGroupUsagesFromLayoutEntries(t *testing.T) {
	t.Parallel()
	_, _, device := newTestDeviceWithTracker(t)
	defer device.Release()

	uniform := newUsageScopeBuffer(t, device, "uniform", BufferUsageUniform)
	storage := newUsageScopeBuffer(t, device, "storage", BufferUsageStorage)
	readOnly := newUsageScopeBuffer(t, device, "read-only", BufferUsageStorage)
	sampled, sampledView := newUsageScopeTexture(t, device, "sampled", TextureUsageTextureBinding)
	written, writtenView := newUsageScopeTexture(t, device, "written", TextureUsageStorageBinding)

	bufferEntry := func(binding uint32, bindingType gputypes.BufferBindingType) gputypes.BindGroupLayoutEntry {
		return gputypes.BindGroupLayoutEntry{
			Binding:    binding,
			Visibility: ShaderStageCompute,
			Buffer:     &gputypes.BufferBindingLayout{Type: bindingType},
		}
	}
	layout, err := device.CreateBindGroupLayout(&BindGroupLayoutDescriptor{
		Entries: []gputypes.BindGroupLayoutEntry{
			bufferEntry(0, gputypes.BufferBindingTypeUniform),
			bufferEntry(1, gputypes.BufferBindingTypeStorage),
			bufferEntry(2, gputypes.BufferBindingTypeReadOnlyStorage),
			{
				Binding:    3,
				Visibility: ShaderStageCompute,
				Texture: &gputypes.TextureBindingLayout{
					SampleType:    gputypes.TextureSampleTypeFloat,
					ViewDimension: gputypes.TextureViewDimension2D,
				},
			},
			{
				Binding:    4,
				Visibility: ShaderStageCompute,
				StorageTexture: &gputypes.StorageTextureBindingLayout{
					Access:        gputypes.StorageTextureAccessWriteOnly,
					Format:        TextureFormatRGBA8Unorm,
					ViewDimension: gputypes.TextureViewDimension2D,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateBindGroupLayout: %v", err)
	}
	defer layout.Release()
	group, err := device.CreateBindGroup(&BindGroupDescriptor{
		Layout: layout,
		Entries: []BindGroupEntry{
			{Binding: 0, Buffer: uniform, Size: 256},
			{Binding: 1, Buffer: storage, Size: 256},
			{Binding: 2, Buffer: readOnly, Size: 256},
			{Binding: 3, TextureView: sampledView},
			{Binding: 4, TextureView: writtenView},
		},
	})
	if err != nil {
		t.Fatalf("CreateBindGroup: %v", err)
	}
	defer group.Release()

	wantBuffers := map[*core.Buffer]track.BufferUses{
		uniform.core:  track.BufferUsesUniform,
		storage.core:  track.BufferUsesStorageWrite,
		readOnly.core: track.BufferUsesStorageRead,
	}
	if got := len(group.usages.Buffers); got != len(wantBuffers) {
		t.Fatalf("buffer usages = %d, want %d", got, len(wantBuffers))
	}
	for _, use := range group.usages.Buffers {
		if want := wantBuffers[use.Buffer]; use.Usage != want {
			t.Errorf("buffer %q usage = %v, want %v", use.Buffer.Label(), use.Usage, want)
		}
	}
	wantTextures := map[*core.Texture]track.TextureUses{
		sampled.coreTexture: track.TextureUsesResource,
		written.coreTexture: track.TextureUsesStorageWrite,
	}
	if got := len(group.usages.Textures); got != len(wantTextures) {
		t.Fatalf("texture usages = %d, want %d", got, len(wantTextures))
	}
	for _, use := range group.usages.Textures {
		if want := wantTextures[use.Texture]; use.Usage != want {
			t.Errorf("texture %q usage = %v, want %v", use.Texture.Label(), use.Usage, want)
		}
	}
}

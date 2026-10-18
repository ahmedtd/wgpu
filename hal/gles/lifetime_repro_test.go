//go:build integration && linux && !(js && wasm)

package gles

import (
	"runtime"
	"testing"

	"github.com/gogpu/wgpu/hal/gles/egl"
	"github.com/gogpu/wgpu/hal/gles/gl"
)

// newTestAdapterContext creates a real EGL/GL context wrapped in an
// AdapterContext, or skips when no EGL display is available.
func newTestAdapterContext(t *testing.T) *AdapterContext {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)

	if err := egl.Init(); err != nil {
		t.Skipf("egl.Init() failed: %v", err)
	}
	config := egl.DefaultContextConfig()
	config.GLES = false
	eglCtx, err := egl.NewContext(config)
	if err != nil {
		t.Skipf("egl.NewContext() failed: %v", err)
	}
	if err := eglCtx.MakeCurrent(); err != nil {
		eglCtx.Destroy()
		t.Fatalf("MakeCurrent failed: %v", err)
	}
	glCtx := &gl.Context{}
	if err := glCtx.Load(egl.GetGLProcAddress); err != nil {
		eglCtx.Destroy()
		t.Fatalf("GL load failed: %v", err)
	}
	_ = egl.MakeCurrent(eglCtx.Display(), egl.NoSurface, egl.NoSurface, egl.NoContext)
	return NewAdapterContext(eglCtx, glCtx, true)
}

// TestSurfaceDoesNotDestroyAdoptedContext is the regression test for the
// shutdown panic: when a Surface had to create its own EGL context (Wayland /
// window-kind mismatch), the context is now adopted by the Instance, so
// Surface.Destroy must NOT free it while the Device still uses it.
//
// Before the fix, Surface.Destroy called AdapterContext.Destroy (ownsContext
// was true), deleting the context that the Device held. A later Device.Destroy
// locked a nil EGL context and dereferenced nil *gl.Context -> SIGSEGV.
func TestSurfaceDoesNotDestroyAdoptedContext(t *testing.T) {
	adapterCtx := newTestAdapterContext(t)

	inst := &Instance{}
	if prev := inst.adoptContext(adapterCtx); prev != nil {
		t.Fatalf("adoptContext returned unexpected previous context %p", prev)
	}

	// Surface returned by CreateSurface Path B: shares the adopted context.
	surf := &Surface{ctx: adapterCtx, ownsContext: false}
	device := &Device{ctx: adapterCtx, vao: 1}

	// Window close destroys the Surface first.
	surf.Destroy()

	// The shared context must still be alive for the Device.
	if adapterCtx.eglCtx == nil || adapterCtx.gl == nil {
		t.Fatal("Surface.Destroy freed the context the Device still uses")
	}

	// Shutdown destroys the Device next — must not nil-deref.
	device.Destroy()
	if device.vao != 0 {
		t.Fatalf("Device.Destroy left vao set: %d", device.vao)
	}

	// Instance is released last and frees the context.
	inst.Destroy()
	if adapterCtx.eglCtx != nil || adapterCtx.gl != nil {
		t.Fatal("Instance.Destroy did not release the adopted context")
	}
}

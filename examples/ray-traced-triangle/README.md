# ray-traced-triangle

Port of Rust wgpu's
[`examples/features/src/ray_traced_triangle`](https://github.com/gfx-rs/wgpu/tree/trunk/examples/features/src/ray_traced_triangle).
It builds one BLAS (a single triangle) and a TLAS with three instances of it.
A compute shader traces one inline ray query per pixel into a storage texture,
and a fullscreen pass blits that texture into the render target.

The Rust example opens a window. This port renders one frame headlessly at
1024×768, like the Rust example's test mode. It writes the frame to a PNG and
compares it to `screenshot.png`, the Rust reference image, using a mean
difference threshold of 0.02.

**Status:** this example is written against the public ray tracing API
planned in [`docs/RAY-TRACING-PLAN.md`](../../docs/RAY-TRACING-PLAN.md), which
doesn't exist yet. Until it does, the example only builds with `-tags rtwip`.
Without the tag you get a stub that prints a message and exits.

```bash
GOGPU_GRAPHICS_API=vulkan go run -tags rtwip ./examples/ray-traced-triangle -o out.png
# GOGPU_DEBUG=1 enables validation layers; -threshold -1 skips the comparison.
```

`shader.wgsl`, `blit.wgsl` and `screenshot.png` are copied unchanged from
gfx-rs/wgpu (MIT OR Apache-2.0, Copyright (c) The gfx-rs developers).

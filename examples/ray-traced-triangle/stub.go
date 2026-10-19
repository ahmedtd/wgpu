// Copyright 2026 The GoGPU Authors
// SPDX-License-Identifier: MIT

//go:build !rtwip

// Command ray-traced-triangle is a port of Rust wgpu's ray_traced_triangle
// example. It is written against the public ray tracing API planned in
// docs/RAY-TRACING-PLAN.md and only builds with -tags rtwip until that API
// exists (plan task 3.8 removes this stub).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ray-traced-triangle: the public ray tracing API is not implemented yet; see docs/RAY-TRACING-PLAN.md")
	os.Exit(1)
}

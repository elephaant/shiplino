// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Static files only: the app is embedded in the Go binary and served by
  // the local daemon. No server runtime, API routes or image optimization.
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
  reactStrictMode: true,
  poweredByHeader: false,
};

export default nextConfig;

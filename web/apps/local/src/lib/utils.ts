// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Harness entry: bundles the mod's pure scene for a classic <script> (file://
// blocks module fetches). Rebuild after editing scene.ts or tokens.ts:
//   bun build design/sageox-flow/entry.ts --outfile design/sageox-flow/scene.js --format iife
import * as scene from '../../internal/usagepane/mod/hooks/scene'

;(globalThis as unknown as { scene: typeof scene }).scene = scene

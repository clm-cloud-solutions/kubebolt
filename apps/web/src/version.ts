// Product version string shown in the UI chrome (sidebar header,
// About modal, login page footer).
//
// Single source of truth: `apps/web/package.json` `version` field.
// Vite resolves JSON imports natively and tsconfig has
// `resolveJsonModule: true`, so a named import compiles cleanly
// and tree-shaking keeps only the referenced field in the final
// bundle.
//
// Previously this constant was hardcoded and the release process
// required updating it manually alongside the helm chart bumps.
// That drift bit us when RC1 shipped with the UI still reading
// "1.9.0" because nobody touched this file when cutting the
// version bump. Auto-deriving from package.json removes the
// failure mode: `npm version` (or a manual edit of package.json's
// `version`) is now the only place that needs updating.
import { version } from '../package.json'

export const VERSION = version

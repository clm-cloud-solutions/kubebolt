/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Optional remote API origin (e.g. https://api.kubebolt.io). Empty/undefined
   *  keeps every call same-origin via the nginx proxy. Set for Vercel deploys. */
  readonly VITE_API_URL?: string
}

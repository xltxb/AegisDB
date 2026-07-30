// Business result codes carried in the { code, msg, data } envelope.
//
// They live in their own side-effect-free module so pure logic that has to
// interpret them (see lib/execOutcome) can be imported and unit-tested without
// dragging in the axios client, which needs a bundler-provided import.meta.env.
// api/http re-exports them, so existing imports are unaffected.
export const CODE_OK = 0
export const CODE_INTERCEPTED = 42200
export const CODE_SCRIPT_PATH_UNSET = 42600
export const CODE_EXPORT_PATH_UNSET = 42601
export const CODE_MFA_REQUIRED = 42800

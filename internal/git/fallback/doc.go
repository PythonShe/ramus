// Package fallback holds no code: its tests open repositories the engine
// does not read natively (reftable refs, SHA-256 objects) and check that
// everything the app needs still works through git, so the fallback is a
// tested path rather than an untested promise.
package fallback

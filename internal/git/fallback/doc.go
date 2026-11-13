// Package fallback holds no code: its tests open repositories the engine
// does not read natively (SHA-256 objects, with files or reftable refs) and check that
// everything the app needs still works through git, so the fallback is a
// tested path rather than an untested promise.
package fallback

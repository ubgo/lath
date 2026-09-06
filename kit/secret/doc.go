// Package secret holds credentials and the operations a pipeline performs on
// them: carrying one without leaking it, grouping them, and reconciling a
// group against wherever they must be stored.
//
// The organising idea is that the careless path must be the correct path. A
// Value cannot be printed, logged, or serialised by accident, every standard
// Go path that would disclose it is closed, and Reveal is the only door. That
// name is deliberate: auditing what escapes means grepping for one word.
//
// Nothing here names a vendor. A Store is an interface; GitHub, Vault, and
// anything else are adapters written elsewhere.
package secret

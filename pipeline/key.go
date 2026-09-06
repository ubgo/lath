package pipeline

// Key names one value that flows from the step producing it to the steps
// consuming it.
//
// Why a distinct type rather than a bare string: Requires and Provides are
// compared against each other by Validate, so a typo on either side must be
// impossible to introduce silently. A defined type means the compiler rejects
// an untyped literal wherever a Key is expected, and every legal value is
// greppable from the one file that declares them.
//
// Invariant: this package declares the TYPE only. Concrete keys are declared
// by whichever package owns the domain (see steps.KeyCommit and friends),
// because the engine has no opinion about what flows through it.
type Key string

// String renders the key for messages. Defined explicitly rather than relying
// on %v so error text stays stable if Key ever gains structure.
func (k Key) String() string { return string(k) }

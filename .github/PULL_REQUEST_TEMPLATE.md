**What this changes, and why**

<!-- The why is the part that cannot be recovered from the diff. If this reverses a decision documented somewhere, say so and where. -->

**Placement** <!-- delete if not adding a package or a step -->

- [ ] It does **not** import `pipeline` → it is in `kit/`
- [ ] It **does** import `pipeline` → it is in `steps/`
- [ ] If it is a step: two projects would not want it to mean different things (otherwise ship the pieces and let each project decide — see CONTRIBUTING.md)

**Checks**

- [ ] `task check` passes — fmt, vet, race tests across every module, cross-build for linux/darwin/windows, `docverify`, `sigverify`
- [ ] `task cover` does not go down; `task cover:check` agrees with the README badge
- [ ] New exported symbols have a doc comment carrying the *why* and any invariant a caller must respect
- [ ] New behaviour has a test whose comment states what breaks in the real world if the assertion stops holding
- [ ] Reference docs updated for anything exported (`docverify` fails otherwise, in both directions)
- [ ] No suppressed diagnostics — no `//nolint`, no skipped test, no loosened assertion

**Anything you are unsure about**

<!-- Especially welcome. A pull request that names its own weak spot gets a better review than one that hides it. -->

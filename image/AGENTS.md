# Commitments

go.podman.io/image/v5 (aka c/image) promises to keep a stable Go API,
following the general Go semantic versioning rules, and not bumping
the major version. Keep that promise.

# Prioritize human attention

Avoid repetitive code. As a rule of thumb, 3 repetitions of
the same >5-line pattern should probably be refactored, _if_
a clear abstraction can be found.

Human’s screens (and attention spans) are limited. Avoid very long
linear functions, look for ways to abstract / split the function
_if_ the resulting smaller functions have a clear purpose and interface.
Use blank lines within function bodies sparingly, less than
you would do by default (but do use them when separating large conceptually
different parts of the function’s code).

Don't add redundant comments that add no value. Code in style
```go
// Add a user
….addUser(…)
```
is _never_ acceptable.

# Tests

The default pattern is TestFunctionName, or TestObjectMethodName
(in that case, with no underscore), containing all tests for a function/method.
Do not _default_ to adding a new test function when adding a feature
to an existing function.

Tests should typically be table-driven. When choosing between a
semantically precise table types and short table entries, prefer short table entries
so that the whole test table easily fits on a screen. For example, usually don't
add .name fields to test tables - have such descriptions as comments on the same line
as the other test content.

It should be very rare to test error message text. Just a test that an error is reported
is frequently enough.

# Existing code

Have some (but not slavish) deference to existing code structure: don't
refactor a whole file just to add 3 lines, if that addition would be otherwise clean.

If some refactoring _is_ beneficial, the goal is to have one or more _pure_
refactoring commits (which don't change the observable behavior at all, and document that
in the commit message), followed by a separate commit that adds the required feature.

# Documentation

Most data structures with scope larger than a single function probably need documentation.
Document field interactions:
```go
UseTLS bool
TLSConfig … // Only used if UseTLS
```
special values:
```go
Name … // "" if unknown
```
_Never_ add comments that add no value:
```go
// A user
type User struct { …}
```
or
```go
Name // name
```
is never acceptable.

Most functions should have documentation, documenting in enough detail that, when working
on a caller code, humans can read only the callee’s documentation without reading the callee
itself — but no more! The documentation of a function should almost never contain _how_
the function does it, the caller shouldn’t need to care.

If such a function documentation looks too convoluted, that’s a sign that the function’s interface
is probably not right — the function boundary is in the wrong place, or an abstraction is missing
to simplify the concepts.

Comments within function bodies should typically only document non-obvious implementation
decisions, non-obvious constraints that require using one approach over another, or _sometimes_
delineate significantly conceptually separate parts of the same function (definitely not
after every blank line). It should not generally be necessary to document, in a caller, what calling another
function does — if that is confusing, the callee should probably be renamed or refactored.

# Commit messages

Never add trailers or attribution lines for coding agents to commit messages or PR descriptions
(for example `Co-authored-by:`, `Assisted-by:`, `Generated-by:`, or "Generated with ..." lines).
Only the human contributor's `Signed-off-by:` is allowed.

#!/bin/sh
# A build pipeline that subdivides its `build` phase with `grotto mark --child`,
# and deliberately leaves one step unmarked to show how grotto surfaces
# unaccounted time. Run it under grotto:
#   grotto run -- tests/fixtures/nested-build-script.sh
# Then read the waterfall:
#   grotto show <trace-id>     # or: grotto tui
#
# A `--child` mark nests one level under the most recent non-child mark,
# subdividing that section. The 60ms of work between `grotto mark build` and the
# first `--child` mark is unmarked, so it renders as a `(gap)` row under build —
# exactly the kind of "where did the time actually go" step that would otherwise
# vanish.
set -e

grotto mark build
sleep 0.06                     # unmarked setup (e.g. `go vet`) -> shows as (gap)

grotto mark compile --child    # subdivides the build section
sleep 0.10

grotto mark link --child
sleep 0.05

grotto mark test               # a new top-level section ends the build subdivision
sleep 0.08

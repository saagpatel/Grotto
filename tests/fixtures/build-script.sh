#!/bin/sh
# A sample build pipeline instrumented with grotto marks. Run it under grotto:
#   grotto run -- tests/fixtures/build-script.sh
# Each `grotto mark` opens a child span that ends at the next mark, so the five
# marks below produce five child spans under one root (six spans total).
set -e

grotto mark setup
sleep 0.05

grotto mark compile
sleep 0.10

grotto mark test
sleep 0.08

grotto mark package
sleep 0.04

grotto mark report
sleep 0.03

#!/bin/sh

# test basic behavior: input from stdin, output to file

${HOLO_BUILD} --format=debian < ${INPUT_TOML}
file package_1.0-1_all.deb

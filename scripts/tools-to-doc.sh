#!/bin/bash

# Find the directory we exist within
DIR=$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )
cd ${DIR}/../build

cat << EOF
# Tools

Metrictank comes with a bunch of helper tools.

Here is an overview of them all.

This file is generated by [tools-to-doc](https://github.com/raintank/metrictank/blob/master/scripts/tools-to-doc.sh)

---

EOF

for tool in mt-*; do
	echo
	echo "## $tool"
	echo
	echo '```'
	./$tool -h 2>&1
	echo '```'
	echo
done
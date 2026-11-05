#!/bin/sh
# Fail if the tree contains a home-directory prefix or an email address.
# The needle is assembled so this file itself does not contain it.
set -eu
root=${1:-.}
needle=$(printf '%s%s' '/User' 's/')
if grep -R -n -I -F --exclude-dir=.git --exclude-dir=dist "$needle" "$root"; then
	echo "personal path found" >&2
	exit 1
fi
if grep -R -n -I -E --exclude-dir=.git --exclude-dir=dist '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' "$root"; then
	echo "email address found" >&2
	exit 1
fi
exit 0

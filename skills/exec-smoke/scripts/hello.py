"""exec-smoke: prints to stdout and writes an artifact to WORKSPACE_DIR.

Runs under the exec service's sandbox. Demonstrates the install+run path and
the output-detection feature.
"""

import os
import sys

from tabulate import tabulate

print("hello from exec-smoke")
print(tabulate([["python", sys.version.split()[0]]], headers=["tool", "version"]))

workspace = os.environ.get("WORKSPACE_DIR")
if not workspace:
    sys.exit("WORKSPACE_DIR not set")

with open(os.path.join(workspace, "note.txt"), "w", encoding="utf-8") as f:
    f.write("exec-smoke ran OK\n")

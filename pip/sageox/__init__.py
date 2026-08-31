"""SageOx (ox) CLI wrapper for the Python ecosystem.

SKELETON — follow-up to the primary npm wrapper. This package does not build or
vendor a second binary; it downloads the official signed ``ox`` release binary
and verifies it against the release ``checksums.txt`` (the same integrity check
as ``scripts/install.sh``).
"""

# Keep in sync with internal/version/version.go via scripts/version-bump.sh.
__version__ = "0.14.3"

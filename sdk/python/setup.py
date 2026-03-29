# Legacy setup.py shim — all configuration is in pyproject.toml.
# This file exists only for backward compatibility with older pip versions
# that do not read pyproject.toml directly.
from setuptools import setup
setup()

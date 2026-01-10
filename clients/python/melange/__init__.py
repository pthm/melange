"""
Melange - Python client for PostgreSQL authorization.

Melange is an OpenFGA-compatible authorization library that runs entirely
in PostgreSQL. This Python client provides type-safe access to the
authorization system.

Example:
    >>> from melange import Checker, Object
    >>> checker = Checker(pool)
    >>> decision = await checker.check(
    ...     subject=Object(type="user", id="123"),
    ...     relation="can_read",
    ...     object=Object(type="repository", id="456"),
    ... )

Note:
    This package is a placeholder. The Python runtime is not yet implemented.
    Use the Go runtime (github.com/pthm/melange/melange) for production.
"""

from melange.types import Decision, Object, ObjectType, Relation
from melange.checker import Checker

__all__ = [
    "Checker",
    "Decision",
    "Object",
    "ObjectType",
    "Relation",
]

__version__ = "0.0.0"

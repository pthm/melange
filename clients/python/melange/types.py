"""
Melange type definitions.

This module provides type definitions for Melange authorization checks.
Types are designed to match the Go runtime for cross-language consistency.
"""

from dataclasses import dataclass
from typing import NewType

# Type aliases for clarity
ObjectType = NewType("ObjectType", str)
"""An authorization object type (e.g., 'user', 'repository')."""

Relation = NewType("Relation", str)
"""An authorization relation (e.g., 'owner', 'can_read')."""


@dataclass(frozen=True, slots=True)
class Object:
    """An authorization object with type and ID.

    Attributes:
        type: The object type (e.g., 'user', 'repository').
        id: The unique identifier for this object.

    Example:
        >>> user = Object(type=ObjectType("user"), id="123")
        >>> repo = Object(type=ObjectType("repository"), id="456")
    """

    type: ObjectType
    id: str


@dataclass(frozen=True, slots=True)
class Decision:
    """The result of a permission check.

    Attributes:
        allowed: Whether the permission was granted.

    Example:
        >>> decision = Decision(allowed=True)
        >>> if decision.allowed:
        ...     print("Access granted")
    """

    allowed: bool

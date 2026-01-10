"""
Melange Checker implementation.

This module provides the Checker class for performing authorization checks.
Currently a placeholder - implementation coming in a future release.
"""

from typing import Any

from melange.types import Decision, Object, Relation


class Checker:
    """Performs authorization checks against a PostgreSQL database.

    Example:
        >>> import asyncpg
        >>> from melange import Checker, Object
        >>>
        >>> pool = await asyncpg.create_pool(dsn="postgresql://...")
        >>> checker = Checker(pool)
        >>>
        >>> decision = await checker.check(
        ...     subject=Object(type="user", id="123"),
        ...     relation="can_read",
        ...     object=Object(type="repository", id="456"),
        ... )

    Note:
        This is a placeholder implementation. The Python runtime is not yet
        implemented. Use the Go runtime for production workloads.
    """

    def __init__(self, db: Any) -> None:
        """Create a new Checker instance.

        Args:
            db: A PostgreSQL connection pool (asyncpg or psycopg).

        Raises:
            NotImplementedError: Always, as this is a placeholder.
        """
        raise NotImplementedError(
            "Melange Python runtime is not yet implemented. "
            "Use the Go runtime (github.com/pthm/melange/melange) for now."
        )

    async def check(
        self,
        subject: Object,
        relation: Relation,
        object: Object,
    ) -> Decision:
        """Perform a permission check.

        Args:
            subject: The subject requesting access.
            relation: The relation to check.
            object: The object being accessed.

        Returns:
            A Decision indicating whether access is allowed.

        Raises:
            NotImplementedError: Always, as this is a placeholder.
        """
        raise NotImplementedError("Not implemented")

"""Errors raised by Client."""

from typing import Optional


class LanternError(Exception):
    """Base error: carries the daemon HTTP status when known."""

    def __init__(self, message: str, status: Optional[int] = None):
        super().__init__(message)
        self.status = status


class AuthError(LanternError):
    """401: missing or invalid daemon token."""


class NotFoundError(LanternError):
    """404: unknown transfer ID."""


class TransferFailed(LanternError):
    """Transfer reached a failed/canceled terminal state."""

    def __init__(self, record):
        super().__init__(record.error or f"transfer {record.state}: {record.id}")
        self.record = record

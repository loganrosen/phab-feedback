"""Public error types."""


class PhabFeedbackError(Exception):
    """Base error suitable for display to CLI users."""


class ConfigurationError(PhabFeedbackError):
    """Configuration or credential resolution failed."""


class NetworkError(PhabFeedbackError):
    """A remote request failed."""

    def __init__(self, message: str, *, status: int | None = None) -> None:
        super().__init__(message)
        self.status = status


class APIError(PhabFeedbackError):
    """A remote API rejected an operation."""


class ValidationError(PhabFeedbackError):
    """A user-supplied identifier or operation was invalid."""
